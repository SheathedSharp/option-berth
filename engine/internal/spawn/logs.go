package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
)

// Detached-run log rotation: 10 MiB per file, three generations named
// `<name>.log`, `<name>.log.1` and `<name>.log.2`.
const (
	LogMaxBytes int64 = 10 << 20
	LogKeep           = 3
)

// LogDir is where a detached run's logs land.
func LogDir() string { return paths.Logs() }

// LogPath is where a detached run's stdout and stderr land:
// <dir>/logs/<group>/<name>.log.
func LogPath(group, name string) string {
	return filepath.Join(
		LogDir(),
		sanitizeSegment(defaulted(group, "ungrouped")),
		sanitizeSegment(defaulted(name, "run"))+".log",
	)
}

func defaulted(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// OpenLog opens a run's log for appending, rotating it first if it has reached
// LogMaxBytes. A detached child holds the descriptor for its whole life, so
// rotation happens at open rather than mid-write: renaming under a running
// writer would leave it writing to a file nobody can find.
func OpenLog(logPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, fmt.Errorf("creating the log directory: %w", err)
	}
	if err := Rotate(logPath); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", logPath, err)
	}
	return file, nil
}

// Rotate shifts <logPath> to <logPath>.1 (and .1 to .2, dropping the oldest)
// once it has reached LogMaxBytes. A missing or small file is left alone.
func Rotate(logPath string) error {
	info, err := os.Stat(logPath)
	if err != nil || info.Size() < LogMaxBytes {
		return nil
	}
	for generation := LogKeep - 1; generation >= 1; generation-- {
		older := fmt.Sprintf("%s.%d", logPath, generation)
		if generation == LogKeep-1 {
			_ = os.Remove(older)
		}
		source := logPath
		if generation > 1 {
			source = fmt.Sprintf("%s.%d", logPath, generation-1)
		}
		if _, err := os.Stat(source); err != nil {
			continue
		}
		if err := os.Rename(source, older); err != nil {
			return fmt.Errorf("rotating %s: %w", source, err)
		}
	}
	return nil
}

// sanitizeSegment keeps a group or service name usable as one path element.
func sanitizeSegment(value string) string {
	value = strings.TrimSpace(value)
	replace := func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\\:*?"<>|`, r) {
			return '-'
		}
		return r
	}
	value = strings.Map(replace, value)
	value = strings.Trim(value, ". ")
	if value == "" {
		return "run"
	}
	return value
}
