package groups

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// One selected file's evidence, not a second parsed model. data is the exact
// input to parse and stays private; readers receive immutable Config pointers.
// Missing/error results are always rechecked, never renewed by equal metadata.
type manifestObservation struct {
	path    string
	data    []byte
	readErr error
	cfg     *Config
	err     error // read or parse error, exposed through Invalid
}

func selectManifest(dir string) manifestObservation {
	for _, name := range configNames {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			// Do not fall back to a lower-priority declaration when the
			// preferred name could not be inspected.
			return manifestObservation{path: path, readErr: err}
		}
		if info.IsDir() {
			continue // same fixed-name selection rule as FilesIn
		}
		return readManifest(path)
	}
	return manifestObservation{}
}

func readManifest(path string) manifestObservation {
	path = Canonical(path)
	result := manifestObservation{path: path}
	// Reject an already-observed special file before opening: a manifest is
	// not a pipe/device stream. This is not a hostile-filesystem race barrier.
	info, err := os.Stat(path)
	if err != nil {
		result.readErr = err
		return result
	}
	if !info.Mode().IsRegular() {
		result.readErr = fmt.Errorf("manifest %s is not a regular file", path)
		return result
	}
	result.data, result.readErr = os.ReadFile(path)
	return result
}

func sameManifest(a, b manifestObservation) bool {
	if a.path != b.path || (a.readErr == nil) != (b.readErr == nil) {
		return false
	}
	if a.readErr != nil {
		// Only suppress repeated diagnostics; both reads were still attempted.
		return a.readErr.Error() == b.readErr.Error()
	}
	return bytes.Equal(a.data, b.data)
}

// acceptManifest atomically reconciles the index's lookup views under its
// existing caller-owned synchronization. No partially decoded Config escapes.
func (x *Index) acceptManifest(dir string, next manifestObservation, explicit bool) error {
	previous, known := x.files[dir]
	if known && sameManifest(previous, next) {
		if next.path == "" {
			x.forgetMissingDirectory(dir)
		}
		return previous.err
	}
	if next.path == "" && !known && !explicit {
		return nil // do not retain every negative ancestor of every process
	}
	next.err = next.readErr
	if next.path != "" && next.readErr == nil {
		next.cfg, next.err = parse(next.path, next.data)
	}

	x.removeManifest(previous.path)
	x.removeManifest(next.path)
	if previous.path != "" {
		delete(x.invalid, previous.path)
	}
	// Direct AddFile/LoadFile must revoke an earlier Add or another spelling,
	// not leave valid and invalid views of the same directory simultaneously.
	if cfg := x.configs[dir]; cfg != nil {
		delete(x.configs, dir)
		x.orderedDirty, x.claimsDirty = true, true
	}
	if next.cfg != nil {
		x.Add(next.cfg)
	}
	if next.err != nil {
		x.invalid[next.path] = next.err
	}
	x.files[dir] = next

	if previous.path != "" && previous.path != next.path {
		// Two manifest symlinks may name the same canonical file. Retiring
		// one selection must not erase another still-observed contribution.
		for other, observation := range x.files {
			if other != dir && observation.path == previous.path {
				if observation.cfg != nil && x.configs[observation.cfg.Dir] == nil {
					x.Add(observation.cfg)
				}
				if observation.err != nil {
					x.invalid[observation.path] = observation.err
				}
			}
		}
	}
	if next.path == "" {
		x.forgetMissingDirectory(dir)
	}
	return next.err
}

func (x *Index) removeManifest(path string) {
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if cfg := x.configs[dir]; cfg != nil && cfg.Path == path {
		delete(x.configs, dir)
		x.orderedDirty, x.claimsDirty = true, true
	}
}

func (x *Index) forgetMissingDirectory(dir string) {
	if info, err := os.Stat(dir); os.IsNotExist(err) || (err == nil && !info.IsDir()) {
		delete(x.files, dir)
	}
}
