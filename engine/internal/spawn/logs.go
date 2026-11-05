package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
)

// A detached run writes into a log that is capped and rotated rather than left
// to grow for as long as the run lives.
const (
	LogMaxBytes int64 = 10 << 20
	LogKeep           = 3
)

// LogDir is the directory detached logs live under.
func LogDir() string { return paths.Logs() }

// LogPath names one run's log: <logs>/<group>/<name>.log. Both segments come
// from user input, so each is reduced to something a filesystem accepts.
func LogPath(group, name string) string {
	dir := filepath.Join(LogDir(), sanitizeSegment(orDefault(group, "ungrouped")))
	return filepath.Join(dir, sanitizeSegment(orDefault(name, "run"))+".log")
}

// orDefault is the placeholder a nameless run is filed under.
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// OpenLog opens a run's log for appending, rotating first when it has reached
// LogMaxBytes.
//
// Rotation happens here rather than during a write because the child holds the
// descriptor for its whole life: renaming the file underneath a running writer
// would leave it writing somewhere nobody can find.
func OpenLog(path string) (*os.File, error) {
	if err := prepareLogDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := Rotate(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return file, nil
}

// prepareLogDir creates the directory a run's log will land in. It is private
// to the user: a log can quote a service's own output back verbatim.
func prepareLogDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating the log directory: %w", err)
	}
	return nil
}

// Rotate moves <path> to <path>.1 (and .1 to .2, dropping the oldest) once it
// has reached LogMaxBytes. A missing or small file is left alone.
func Rotate(path string) error {
	size, err := sizeOf(path)
	if err != nil || size < LogMaxBytes {
		return nil
	}
	// Oldest slot first: the drop happens before the shifts fill it back up.
	_ = os.Remove(generation(path, LogKeep-1))
	for slot := LogKeep - 1; slot >= 1; slot-- {
		if err := shift(generation(path, slot-1), generation(path, slot)); err != nil {
			return err
		}
	}
	return nil
}

// generation names one rotation slot; slot 0 is the log itself.
func generation(path string, slot int) string {
	if slot == 0 {
		return path
	}
	return fmt.Sprintf("%s.%d", path, slot)
}

// sizeOf reports a file's length, or the stat error when there is no file.
func sizeOf(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// shift renames source onto target. An absent source counts as done: a policy
// with more slots than the log has generations yet fills from the bottom up.
func shift(source, target string) error {
	if _, err := os.Stat(source); err != nil {
		return nil
	}
	if err := os.Rename(source, target); err != nil {
		return fmt.Errorf("rotating %s: %w", source, err)
	}
	return nil
}

// notInPathElements are characters a path element cannot carry on one platform
// or another; a group or service name may contain any of them.
const notInPathElements = `/\\:*?"<>|`

// sanitizeSegment keeps a group or service name usable as one path element.
func sanitizeSegment(s string) string {
	s = strings.Map(pathSafeRune, strings.TrimSpace(s))
	if s = strings.Trim(s, ". "); s == "" {
		return "run"
	}
	return s
}

// pathSafeRune replaces a character that cannot appear in a path element with
// a dash, which is inert everywhere.
func pathSafeRune(r rune) rune {
	if r < 0x20 || strings.ContainsRune(notInPathElements, r) {
		return '-'
	}
	return r
}
