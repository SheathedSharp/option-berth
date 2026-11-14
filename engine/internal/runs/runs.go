// Package runs keeps the on-disk mirror of the processes option-berth
// started, keyed by pid. Every entry carries the tag (and, for `oberth start`,
// the stable id) that lets `oberth list` attribute a listening port back to
// its owner.
//
// The file sits beside the config, e.g. ~/.option-berth/runs.json. Writers
// serialize through a sidecar lock and finish with an atomic rename, so
// concurrent invocations cannot clobber each other's entries.
package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
)

// Entry is one tagged run. The registry keys entries by PID.
//
// The fields after Tag arrived with `oberth start`. They are omitempty, so a
// file written by an older `option-berth run` still loads: Tag then stands for
// both group and name, which is the migration `run --tag X` -> `start --group
// X` promises.
type Entry struct {
	PID       int    `json:"pid"`
	Tag       string `json:"tag"`
	ID        string `json:"id,omitempty"`
	StartedAt string `json:"startedAt,omitempty"` // RFC3339
	Cmd       string `json:"cmd,omitempty"`
	SpecHash  string `json:"specHash,omitempty"`

	// Placement in the manifest.
	Group string `json:"group,omitempty"`
	Name  string `json:"name,omitempty"`
	Cwd   string `json:"cwd,omitempty"`

	// Launch facts.
	PPID     int `json:"ppid,omitempty"`
	PortHint int `json:"portHint,omitempty"`

	// LogPath and LogOffset are this run's own part of the service log: the
	// file it writes to, and where its output begins in that append-only file.
	// The mirror is the only copy of a live run that survives a runtime service
	// restart, so leaving them out makes `logs --run current` answer "no log
	// file" for a process that is still writing to it.
	LogPath   string `json:"logPath,omitempty"`
	LogOffset int64  `json:"logOffset,omitempty"`

	// Executable is the absolute path of the program this run started. It is
	// what status compares with the run's start time to tell a process running
	// the current artifact from one still on an older build; the mirror keeps
	// it across a runtime service restart.
	Executable string `json:"executable,omitempty"`
}

// GroupOf is the group this run attributes its ports to.
func (e Entry) GroupOf() string { return firstSet(e.Group, e.Tag) }

// NameOf is the service name this run attributes its ports to.
func (e Entry) NameOf() string { return firstSet(e.Name, e.Tag) }

// firstSet returns the first non-empty value, so a legacy entry whose new
// field is absent falls back to its tag.
func firstSet(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// Registry is the in-memory view of the runs file.
type Registry struct {
	Runs map[int]Entry `json:"runs"`
}

// Path is the absolute path of the runs file. The layout belongs to
// internal/paths, not to a copy of it here.
func Path() string { return paths.Runs() }

func lockPath() string { return Path() + ".lock" }

// load reads the registry without pruning. A missing file yields an empty
// registry, and a malformed one is treated as empty: a damaged mirror must
// never break `oberth list`.
func load() *Registry {
	data, err := os.ReadFile(Path())
	if err != nil {
		return emptyRegistry()
	}
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil || reg.Runs == nil {
		return emptyRegistry()
	}
	return &reg
}

func emptyRegistry() *Registry {
	return &Registry{Runs: map[int]Entry{}}
}

// Load reads the registry and drops entries whose process is gone, persisting
// the cleaned file so a crash or hard kill self-heals. A failure to persist is
// ignored: the caller still gets the pruned view.
func Load() *Registry {
	reg := load()
	if !pruneDead(reg) {
		return reg
	}
	_ = withLock(func() error {
		// Re-read under the lock and prune again, so an entry added by a
		// concurrent writer between the first read and the lock survives.
		fresh, err := loadForImport()
		if err != nil {
			return err
		}
		pruneDead(fresh)
		reg = fresh
		return save(fresh)
	})
	return reg
}

// pruneDead removes entries whose PID no longer exists and reports whether it
// removed any.
func pruneDead(reg *Registry) bool {
	removed := false
	for pid := range reg.Runs {
		if !pidAlive(pid) {
			delete(reg.Runs, pid)
			removed = true
		}
	}
	return removed
}

// LookupByPID returns the entry for a PID when one is recorded.
func (r *Registry) LookupByPID(pid int) (Entry, bool) {
	entry, recorded := r.Runs[pid]
	return entry, recorded
}

// Active lists every recorded entry. Load has already pruned the dead ones.
func (r *Registry) Active() []Entry {
	out := make([]Entry, 0, len(r.Runs))
	for _, entry := range r.Runs {
		out = append(out, entry)
	}
	return out
}

// Add records or replaces the entry for e.PID, under the lock.
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

// Remove deletes the entry for pid under the lock. An unrecorded pid is a
// no-op.
func Remove(pid int) error {
	return withLock(func() error {
		reg, err := loadForImport()
		if err != nil {
			return err
		}
		if _, recorded := reg.Runs[pid]; !recorded {
			return nil
		}
		delete(reg.Runs, pid)
		return save(reg)
	})
}

// save writes the registry atomically: marshal, write a temporary file beside
// it, rename into place. The caller must already hold the lock.
//
// The directory is created owner-only. This mirror can be the first writer of
// ~/.option-berth (a CLI run before any runtime service), and it must match the mode
// paths.EnsureDir uses rather than leave a listable directory behind.
func save(reg *Registry) error {
	path := Path()
	if err := ensureConfigDir(path); err != nil {
		return err
	}
	if reg.Runs == nil {
		reg.Runs = map[int]Entry{}
	}
	payload, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("runs: marshal: %w", err)
	}
	return writeAtomic(path, payload)
}

func ensureConfigDir(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("runs: could not create config dir: %w", err)
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runs-*.json")
	if err != nil {
		return fmt.Errorf("runs: temp file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("runs: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("runs: close: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("runs: rename: %w", err)
	}
	return nil
}

// PIDAlive reports whether a process is still running. The runtime service's in-memory
// registry prunes with the same test.
func PIDAlive(pid int) bool { return pidAlive(pid) }
