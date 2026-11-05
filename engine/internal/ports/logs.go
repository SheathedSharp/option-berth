package ports

import (
	"runtime"
	"strconv"
	"strings"
)

// LogSource is one place a process writes output.
type LogSource struct {
	Path string
	// FD is "stdout", "stderr", or empty for a log file.
	FD string
}

// FindLogSources discovers a process's output streams and log files through
// lsof. Windows has no equivalent here, so it returns nil.
func FindLogSources(pid int) []LogSource {
	if runtime.GOOS == "windows" {
		return nil
	}
	cmd, stop := bounded("lsof", "-p", strconv.Itoa(pid), "-Fn", "-a")
	defer stop()
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var sources []LogSource
	seen := make(map[string]bool)
	fd := ""
	for _, record := range strings.Split(string(out), "\n") {
		if record == "" {
			continue
		}
		switch record[0] {
		case 'f':
			fd = record[1:]
		case 'n':
			sources = appendLogSource(sources, record[1:], fd, seen)
		}
	}
	return sources
}

// appendLogSource adds one lsof name record: a stdout/stderr stream when the
// fd says so, otherwise a file that looks like a log. A path is added once.
func appendLogSource(sources []LogSource, path, fd string, seen map[string]bool) []LogSource {
	if fd == "1" || fd == "2" {
		if !isReadableFile(path) || seen[path] {
			return sources
		}
		seen[path] = true
		name := "stdout"
		if fd == "2" {
			name = "stderr"
		}
		return append(sources, LogSource{Path: path, FD: name})
	}
	if !isLogFile(path) || seen[path] {
		return sources
	}
	seen[path] = true
	return append(sources, LogSource{Path: path})
}

// isReadableFile reports whether a path looks like a regular file rather than
// a pipe, socket or device.
func isReadableFile(path string) bool {
	return strings.HasPrefix(path, "/") &&
		!strings.HasPrefix(path, "/dev/") &&
		!strings.Contains(path, "->")
}

// isLogFile reports whether a path looks like a log file.
func isLogFile(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "/dev/") {
		return false
	}
	folded := strings.ToLower(path)
	if strings.Contains(folded, "/log/") || strings.Contains(folded, "/logs/") {
		return true
	}
	return strings.HasSuffix(folded, ".log") || strings.HasSuffix(folded, ".out")
}
