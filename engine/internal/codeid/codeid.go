// Package codeid fingerprints the source files a service is running from.
//
// It answers one question the run registry cannot: is the code on disk the
// same code this run started with? The identity is content-based, not a commit
// id, so uncommitted edits count. In a Git checkout the file set is what Git
// tracks plus the untracked files Git does not ignore — node_modules, target
// and dist do not enter the digest unless the project really treats them as
// source. Outside a checkout a directory walk applies a fixed ignore list.
//
// A per-run snapshot is stored under $BERTH_HOME/code, keyed by run id and
// deleted when that run leaves the registry. It is an input fact for one run,
// not a second registry and not a cached runtime result.
package codeid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
)

// SnapshotVersion is the on-disk encoding of Snapshot. A file from another
// version is unreadable evidence, not a snapshot to compare against.
const SnapshotVersion = 1

const (
	// maxStoredFiles bounds the per-file map kept for exact change counts. Past
	// it the digest still answers "changed", but the file count stays unknown;
	// a huge JSON file is not worth a number that only decorates the answer.
	maxStoredFiles = 20000
	// gitTimeout bounds one Git subprocess. A hung Git must not hang `up`.
	gitTimeout = 15 * time.Second
)

// ignoredInWalk keeps a non-Git source directory from hashing build output and
// dependency trees. It applies below the declared source, never to the source
// itself: `sources: [build]` still fingerprints what the author named.
var ignoredInWalk = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
	".venv":        true,
	"venv":         true,
	"__pycache__":  true,
	".gradle":      true,
	".idea":        true,
	".next":        true,
	".nuxt":        true,
	"coverage":     true,
	".tox":         true,
}

// ResolveSources turns the manifest's `sources:` into the paths to fingerprint.
// A missing key means the service's working directory. An explicit empty list
// means the service is not checked (a hot-reloading service, for example).
func ResolveSources(sources []string) (paths []string, enabled bool) {
	if sources == nil {
		return []string{"."}, true
	}
	if len(sources) == 0 {
		return nil, false
	}
	return append([]string(nil), sources...), true
}

// Snapshot is one content fingerprint of a service's declared sources.
type Snapshot struct {
	Version   int               `json:"version"`
	RunID     string            `json:"run_id,omitempty"`
	Root      string            `json:"root"`
	Sources   []string          `json:"sources"`
	Head      string            `json:"head,omitempty"`
	Digest    string            `json:"digest"`
	FileCount int               `json:"file_count"`
	Files     map[string]string `json:"files,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
}

// Take fingerprints root's declared sources. Root is the service's working
// directory; source paths are relative to it. A missing source is empty, not
// an error: deleting the tree is a change, and the comparison says so.
func Take(ctx context.Context, root string, sources []string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	root = filepath.Clean(root)
	snap := Snapshot{
		Version: SnapshotVersion,
		Root:    root,
		Sources: append([]string(nil), sources...),
		Head:    gitHead(ctx, root),
	}
	files, err := gitFiles(ctx, root, sources)
	if err != nil {
		// Not a checkout, or Git could not answer. The walk is the fallback,
		// not a second source of truth: its result is the same shape.
		files, err = walkFiles(ctx, root, sources)
		if err != nil {
			return Snapshot{}, err
		}
	}
	snap.FileCount = len(files)
	if len(files) <= maxStoredFiles {
		snap.Files = files
	} else {
		snap.Truncated = true
	}
	snap.Digest = digest(files)
	return snap, nil
}

// Compare reports whether the code behind current differs from start. When
// both snapshots kept a per-file map, changedFiles lists exactly the paths
// that appeared, disappeared or changed content; otherwise it is nil and only
// the boolean is known.
func Compare(start, current Snapshot) (changed bool, changedFiles []string) {
	changed = start.Digest != current.Digest
	if start.Files == nil || current.Files == nil {
		return changed, nil
	}
	for path, hash := range current.Files {
		if startHash, ok := start.Files[path]; !ok || startHash != hash {
			changedFiles = append(changedFiles, path)
		}
	}
	for path := range start.Files {
		if _, ok := current.Files[path]; !ok {
			changedFiles = append(changedFiles, path)
		}
	}
	sort.Strings(changedFiles)
	return changed || len(changedFiles) > 0, changedFiles
}

// Path is where one run's snapshot lives. The id is hashed so an id from the
// wire can never choose a path.
func Path(runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(paths.Code(), hex.EncodeToString(sum[:])+".json")
}

// Save writes one run's snapshot atomically with owner-only permissions.
func Save(runID string, snap Snapshot) error {
	if strings.TrimSpace(runID) == "" {
		return errors.New("codeid: empty run id")
	}
	snap.RunID = runID
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("codeid: encoding snapshot: %w", err)
	}
	dir := paths.Code()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("codeid: creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".code-*")
	if err != nil {
		return fmt.Errorf("codeid: creating snapshot: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("codeid: writing snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("codeid: writing snapshot: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("codeid: securing snapshot: %w", err)
	}
	if err := os.Rename(name, Path(runID)); err != nil {
		return fmt.Errorf("codeid: storing snapshot: %w", err)
	}
	return nil
}

// Load reads one run's snapshot. A missing or unreadable snapshot is unknown
// evidence, reported as an error so a reader never mistakes it for "same code".
func Load(runID string) (Snapshot, error) {
	data, err := os.ReadFile(Path(runID))
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("codeid: decoding snapshot: %w", err)
	}
	if snap.Version != SnapshotVersion || snap.RunID != runID {
		return Snapshot{}, fmt.Errorf("codeid: snapshot for run %q is not this encoding", runID)
	}
	return snap, nil
}

// Remove deletes one run's snapshot. A missing file is already removed.
func Remove(runID string) error {
	err := os.Remove(Path(runID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// PruneExcept deletes snapshots whose run is no longer in the live registry.
// It runs at daemon startup: a crash can leave a run's file behind, and the
// files belong to runs, not to the directory.
func PruneExcept(keep map[string]bool) error {
	entries, err := os.ReadDir(paths.Code())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(paths.Code(), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var snap Snapshot
		if json.Unmarshal(data, &snap) != nil || snap.RunID == "" || !keep[snap.RunID] {
			_ = os.Remove(path)
		}
	}
	return nil
}

// gitHead reads the commit checked out at root, or "" when root is not in a
// work tree (or has no commit yet). It is display evidence; the digest decides
// whether code changed.
func gitHead(ctx context.Context, root string) string {
	out, err := gitOut(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// gitFiles lists the tracked and untracked-but-not-ignored files under the
// declared sources. A repository that cannot answer falls back to the walk.
func gitFiles(ctx context.Context, root string, sources []string) (map[string]string, error) {
	if _, err := gitOut(ctx, root, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, err
	}
	args := []string{"ls-files", "-c", "-o", "--exclude-standard", "-z", "--"}
	// Absolute pathspecs keep a source above the service directory (for
	// example `../shared` with cwd `services/api`) inside Git's view. Git
	// rejects a relative `..` pathspec silently, which would make a declared
	// source look empty.
	for _, source := range sources {
		args = append(args, filepath.ToSlash(filepath.Join(root, filepath.FromSlash(source))))
	}
	out, err := gitOut(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		hash, exists, err := hashFile(abs)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		display, err := filepath.Rel(root, abs)
		if err != nil {
			display = abs
		}
		files[filepath.ToSlash(display)] = hash
	}
	return files, nil
}

// walkFiles is the non-Git fallback. It applies ignoredInWalk below each
// declared source and never follows a symlinked directory.
func walkFiles(ctx context.Context, root string, sources []string) (map[string]string, error) {
	files := map[string]string{}
	add := func(abs string) error {
		hash, exists, err := hashFile(abs)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		display, err := filepath.Rel(root, abs)
		if err != nil {
			display = abs
		}
		files[filepath.ToSlash(display)] = hash
		return nil
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		abs := filepath.Join(root, filepath.FromSlash(source))
		info, err := os.Lstat(abs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			if err := add(abs); err != nil {
				return nil, err
			}
			continue
		}
		err = filepath.WalkDir(abs, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				// The declared directory itself is always walked; the ignore
				// list is about what lies below it.
				if path != abs && ignoredInWalk[entry.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return add(path)
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			return add(path)
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// hashFile returns the content hash of one regular file. A symlink hashes its
// target string, so a retargeted link is a change without following it out of
// the source tree. A missing or non-regular entry is not part of the snapshot.
func hashFile(path string) (hash string, exists bool, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return "", false, err
		}
		return stringHash("symlink:" + target), true, nil
	case !info.Mode().IsRegular():
		return "", false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(h.Sum(nil)), true, nil
}

// digest frames the sorted path/hash pairs. The length prefixes keep
// ("ab", "c") and ("a", "bc") distinct, the same framing prepare_inputs uses.
func digest(files map[string]string) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := sha256.New()
	writeField(h, "codeid/v1")
	for _, path := range paths {
		writeField(h, path)
		writeField(h, files[path])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func stringHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func writeField(w io.Writer, s string) {
	fmt.Fprintf(w, "%d:", len(s))
	io.WriteString(w, s)
}

func gitOut(ctx context.Context, root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append([]string{"-C", root, "-c", "core.quotepath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
