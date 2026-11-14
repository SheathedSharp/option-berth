package runtimesvc

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Log rotation policy from the spec's "Error handling" section: each file holds
// 5 MiB and three generations are kept (runtime-service.log, runtime-service.log.1,
// runtime-service.log.2).
const (
	LogMaxBytes = 5 << 20
	LogKeep     = 3
)

// RotatingWriter appends to a file and rotates it once it grows past MaxBytes,
// keeping Keep generations. Concurrent writers may share one.
type RotatingWriter struct {
	Keep     int
	Path     string
	MaxBytes int64

	size int64
	mu   sync.Mutex
	f    *os.File
}

// NewRotatingWriter opens path for appending and creates its directory.
func NewRotatingWriter(path string, maxBytes int64, keep int) (*RotatingWriter, error) {
	err := os.MkdirAll(filepath.Dir(path), 0o700)
	if err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	w := &RotatingWriter{MaxBytes: maxBytes, Keep: keep, Path: path}
	if err = w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	file, err := os.OpenFile(w.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	w.f, w.size = file, stat.Size()
	return nil
}

// Write appends p, rotating first when the file would grow past MaxBytes.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	file := w.f
	if file == nil {
		return 0, os.ErrClosed
	}
	if w.MaxBytes > 0 && w.size+int64(len(p)) > w.MaxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
		file = w.f
	}
	written, err := file.Write(p)
	w.size += int64(written)
	return written, err
}

// rotate renames runtime-service.log.N-1 → runtime-service.log.N and opens a fresh runtime-service.log.
// w.mu is held by the caller.
func (w *RotatingWriter) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	for i := w.Keep - 1; i >= 1; i-- {
		source := w.Path
		if i > 1 {
			source = fmt.Sprintf("%s.%d", w.Path, i-1)
		}
		target := fmt.Sprintf("%s.%d", w.Path, i)
		if _, err := os.Stat(source); err != nil {
			continue
		}
		_ = os.Rename(source, target)
	}
	w.size = 0
	return w.open()
}

// Close closes the file the writer holds open.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	closeErr := w.f.Close()
	w.f = nil
	return closeErr
}

// ParseLogLevel turns the config's runtime_service.log_level into a slog level. Every
// value it does not recognise — the empty string included — means info.
func ParseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "error":
		return slog.LevelError
	case "warn", "warning":
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// NewLogger builds the runtime service's logger. It always writes to the rotated log
// file, so `oberth runtime service log` has something to show, and additionally to
// stderr when the runtime service runs in the foreground.
func NewLogger(path string, level slog.Level, alsoStderr bool) (*slog.Logger, io.Closer, error) {
	file, err := NewRotatingWriter(path, LogMaxBytes, LogKeep)
	if err != nil {
		return nil, nil, err
	}
	var sink io.Writer = file
	if alsoStderr {
		sink = io.MultiWriter(file, os.Stderr)
	}
	handler := slog.NewTextHandler(sink, &slog.HandlerOptions{Level: level})
	return slog.New(handler), file, nil
}
