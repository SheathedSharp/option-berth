package runtimecore

import (
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// DefaultFirstErrorLogBytes bounds how far into a run's log range we scan for
// its first error line. A run that prints more than this before an error falls
// back to the bounded exit tail lines rather than causing an unbounded read.
const DefaultFirstErrorLogBytes int64 = 256 << 10

// FirstErrorPattern is the fixed, case-insensitive vocabulary used to locate
// the first error-bearing log line of a failed run. It matches `oberth logs
// --first-error` so CLI, Runtime World and Attention Notes report the same line.
var FirstErrorPattern = regexp.MustCompile(`(?i)(error|exception|fatal|panic|traceback)`)

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// IsFailureExitReason reports whether a run exit reason represents a failure
// rather than an intentional stop, clean exit or unobserved end.
func IsFailureExitReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "", "stopped", "exited", state.ExitEndedUnobserved:
		return false
	default:
		return true
	}
}

// FindFirstErrorLine returns the first line matching FirstErrorPattern after
// stripping ANSI escapes and normalizing whitespace. Empty means no line
// matched.
func FindFirstErrorLine(lines []string) string {
	for _, raw := range lines {
		clean := strings.TrimRight(ansiEscapePattern.ReplaceAllString(raw, ""), "\r")
		if strings.TrimSpace(clean) == "" {
			continue
		}
		if FirstErrorPattern.MatchString(clean) {
			return sanitizeFirstError(clean)
		}
	}
	return ""
}

// FirstErrorInLogFile scans up to maxBytes of a regular log file starting at
// offset and returns the first error-matching line. Missing, non-regular or
// unreadable files return "" rather than an error.
func FirstErrorInLogFile(path string, offset, maxBytes int64) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	size := info.Size()
	if size <= 0 {
		return ""
	}
	if offset < 0 || offset > size {
		offset = 0
	}
	if maxBytes <= 0 {
		maxBytes = DefaultFirstErrorLogBytes
	}
	limit := size - offset
	if limit > maxBytes {
		limit = maxBytes
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil || len(data) == 0 {
		return ""
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return ""
	}
	return FindFirstErrorLine(strings.Split(text, "\n"))
}

// FirstErrorFromExit prefers the bounded start-of-run scan of logPath and falls
// back to the bounded exit tail lines when the log file is absent, rotated or
// printed more than DefaultFirstErrorLogBytes before failing.
func FirstErrorFromExit(logPath string, logOffset int64, lastLines []string) string {
	if line := FirstErrorInLogFile(logPath, logOffset, DefaultFirstErrorLogBytes); line != "" {
		return line
	}
	return FindFirstErrorLine(lastLines)
}
