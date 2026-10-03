// Package runs implements the tagged-runs registry: a small on-disk JSON file
// recording processes spawned via `option-berth run`, keyed by PID. Each entry carries
// a caller-supplied tag (and optional stable id) so that `oberth list` can
// attribute listening ports back to whoever started them.
//
// The registry lives next to option-berth's config (e.g. ~/.option-berth/runs.json).
// Writes are serialized with a sidecar lock file and an atomic rename so that
// multiple concurrent `option-berth run` invocations don't clobber each other.
package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sheathedsharp/option-berth/internal/paths"
)

// Entry is a single tagged run, keyed in the registry by its PID.
//
// Group, Name, Cwd, PPID and PortHint arrived with `oberth start` (step 1A.5).
// They are all omitempty, so a file written by an older `option-berth run` still
// loads: Tag then stands for both the group and the name, which is exactly the
// migration `option-berth run --tag X` -> `oberth start --group X` promises.
type Entry struct {
	PID       int    `json:"pid"`
	Tag       string `json:"tag"`
	ID        string `json:"id,omitempty"`
	Cmd       string `json:"cmd,omitempty"`
	StartedAt string `json:"startedAt,omitempty"` // RFC3339
	Group     string `json:"group,omitempty"`
	Name      string `json:"name,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	PPID      int    `json:"ppid,omitempty"`
	PortHint  int    `json:"portHint,omitempty"`
	SpecHash  string `json:"specHash,omitempty"`
}

// GroupOf is the group this run attributes its ports to.
func (e Entry) GroupOf() string {
	if e.Group != "" {
		return e.Group
	}
	return e.Tag
}

// NameOf is the service name this run attributes its ports to.
func (e Entry) NameOf() string {
	if e.Name != "" {
		return e.Name
	}
	return e.Tag
}

// Registry is the in-memory view of the on-disk runs file: pid -> entry.
type Registry struct {
	Runs map[int]Entry `json:"runs"`
}

// Path returns the absolute path to the runs registry file, which lives in the
// directory internal/paths owns rather than a layout copied from another package.
func Path() string { return paths.Runs() }

// lockPath returns the sidecar lock file path for the registry.
func lockPath() string {
	return Path() + ".lock"
}

// load reads the registry from path without pruning. A missing file yields an
// empty registry. A malformed file is treated as empty (best-effort recovery)
// rather than an error, so a corrupted registry never breaks `oberth list`.
func load() *Registry {
	reg := &Registry{Runs: map[int]Entry{}}
	data, err := os.ReadFile(Path())
	if err != nil {
		return reg
	}
	if err := json.Unmarshal(data, reg); err != nil || reg.Runs == nil {
		return &Registry{Runs: map[int]Entry{}}
	}
	return reg
}

// Load reads the registry and prunes entries whose PID is no longer alive
// (stale after a crash or hard kill). Pruned entries are written back to disk
// so the file self-heals. Pruning failures to persist are ignored.
func Load() *Registry {
	reg := load()
	pruned := false
	for pid := range reg.Runs {
		if !pidAlive(pid) {
			delete(reg.Runs, pid)
			pruned = true
		}
	}
	if pruned {
		_ = withLock(func() error {
			// Re-read under lock and re-prune to avoid racing a concurrent add.
			fresh, err := loadForImport()
			if err != nil {
				return err
			}
			for pid := range fresh.Runs {
				if !pidAlive(pid) {
					delete(fresh.Runs, pid)
				}
			}
			reg = fresh
			return save(fresh)
		})
	}
	return reg
}

// LookupByPID returns the entry for a PID if present.
func (r *Registry) LookupByPID(pid int) (Entry, bool) {
	e, ok := r.Runs[pid]
	return e, ok
}

// Active returns all live entries (pruning is already applied by Load).
func (r *Registry) Active() []Entry {
	out := make([]Entry, 0, len(r.Runs))
	for _, e := range r.Runs {
		out = append(out, e)
	}
	return out
}

// Add records (or replaces) an entry for e.PID, atomically and under lock.
func Add(e Entry) error {
	if e.PID <= 0 {
		return fmt.Errorf("runs: invalid pid %d", e.PID)
	}
	if e.StartedAt == "" {
		e.StartedAt = time.Now().Format(time.RFC3339Nano)
	}
	return withLock(func() error {
		reg, err := loadForImport()
		if err != nil {
			return err
		}
		reg.Runs[e.PID] = e
		return save(reg)
	})
}

// Remove deletes the entry for pid, atomically and under lock. Removing a
// missing pid is a no-op.
func Remove(pid int) error {
	return withLock(func() error {
		reg, err := loadForImport()
		if err != nil {
			return err
		}
		if _, ok := reg.Runs[pid]; !ok {
			return nil
		}
		delete(reg.Runs, pid)
		return save(reg)
	})
}

// save writes the registry atomically: marshal -> temp file -> rename. Callers
// must already hold the lock (see withLock).
//
// The config directory is created owner-only. This mirror can be the first
// writer of ~/.option-berth — a CLI `oberth run` before any daemon — and it
// must match the mode the daemon's own paths.EnsureDir uses, not leave a
// directory other users can list.
func save(reg *Registry) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("runs: could not create config dir: %w", err)
	}
	if reg.Runs == nil {
		reg.Runs = map[int]Entry{}
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("runs: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runs-*.json")
	if err != nil {
		return fmt.Errorf("runs: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("runs: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("runs: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("runs: rename: %w", err)
	}
	return nil
}

// PIDAlive reports whether a process is still running. The daemon's in-memory
// registry prunes with the same test the on-disk one uses.
func PIDAlive(pid int) bool { return pidAlive(pid) }
