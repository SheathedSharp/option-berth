package runtimesvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// Reading a log file without running a program.
//
// A service's output is a file on this machine, written by the process the
// runtime service started. Reading the last lines of a file is ordinary IO, so it is done
// here rather than by spawning `tail`: the runtime service then has no PATH dependency for
// a fact it already owns, and no platform can be without it.
//
// The follow poll interval is a compromise between latency and syscalls. It is a
// variable so a test does not have to wait for real time.
var followPollInterval = 200 * time.Millisecond

// tailWindow is how much of the end of a file the first attempt reads. It grows
// geometrically until the window holds the requested number of lines or reaches
// the start of the file, so a log whose lines are longer than expected still
// answers exactly.
const tailWindow = 64 << 10

// tailFile returns the last limit lines of path, oldest first. A file with fewer
// lines returns all of them; a file that does not end in a newline keeps its
// last line whole rather than dropping it as unterminated.
func tailFile(path string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	for window := int64(tailWindow); ; window *= 4 {
		start := size - window
		if start < 0 {
			start = 0
		}
		buf := make([]byte, size-start)
		if _, err := file.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		text := string(buf)
		if start > 0 {
			// The window began mid-line. Everything up to the first newline is
			// the tail of a line whose beginning was not read, so it is not a
			// line of this answer.
			cut := strings.IndexByte(text, '\n')
			if cut < 0 {
				text = ""
			} else {
				text = text[cut+1:]
			}
		}
		if lines := lastLines(text, limit); len(lines) >= limit || start == 0 {
			return lines, nil
		}
	}
}

// lastLines splits text into lines and keeps the final limit of them.
func lastLines(text string, limit int) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}

// followFiles streams the last limit lines of each path and then every line
// appended after that, in path order, until ctx ends.
//
// It polls rather than using a platform notification API: the files are small,
// the interval is a fraction of a second, and one implementation that behaves
// the same on every platform is worth more here than a wakeup per write.
func followFiles(ctx context.Context, paths []string, limit int, emit func(string) error) error {
	followers := make([]*fileFollower, 0, len(paths))
	for _, path := range paths {
		follower, err := startFollowing(path, limit)
		if err != nil {
			return err
		}
		for _, line := range follower.backlog {
			if err := emit(line); err != nil {
				return err
			}
		}
		followers = append(followers, follower)
	}

	ticker := time.NewTicker(followPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		for _, follower := range followers {
			if err := follower.poll(emit); err != nil {
				return err
			}
		}
	}
}

// fileFollower is one file being watched: where reading has reached, and the
// fragment of a line written after that point.
type fileFollower struct {
	path    string
	offset  int64
	partial string
	backlog []string
}

// startFollowing reads the backlog a follower emits first and positions the read
// at the end of what it just reported, so the backlog is not repeated.
func startFollowing(path string, limit int) (*fileFollower, error) {
	backlog, err := tailFile(path, limit)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &fileFollower{path: path, offset: info.Size(), backlog: backlog}, nil
}

// poll emits every complete line appended since the last poll. A file that
// shrank was truncated or replaced, so reading restarts at its beginning.
func (f *fileFollower) poll(emit func(string) error) error {
	file, err := os.Open(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // the file is gone; the next write may recreate it
		}
		return err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < f.offset {
		f.offset, f.partial = 0, ""
	}
	if info.Size() == f.offset {
		return nil
	}
	buf := make([]byte, info.Size()-f.offset)
	if _, err := file.ReadAt(buf, f.offset); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	f.offset += int64(len(buf))

	text := f.partial + string(buf)
	cut := strings.LastIndexByte(text, '\n')
	if cut < 0 {
		f.partial = text
		return nil
	}
	f.partial = text[cut+1:]
	for _, line := range strings.Split(text[:cut], "\n") {
		if err := emit(line); err != nil {
			return err
		}
	}
	return nil
}

// logPathsFor is the file list a native process's output can be read from, in
// the order the sources were found.
func logPathsFor(sources []ports.LogSource) []string {
	paths := make([]string, 0, len(sources))
	for _, source := range sources {
		paths = append(paths, source.Path)
	}
	return paths
}

// logLabelsFor names the same sources for the client, naming the descriptor a
// file was opened as when the collector could see it.
func logLabelsFor(sources []ports.LogSource) []string {
	labels := make([]string, 0, len(sources))
	for _, source := range sources {
		if source.FD != "" {
			labels = append(labels, fmt.Sprintf("%s (%s)", source.Path, source.FD))
			continue
		}
		labels = append(labels, source.Path)
	}
	return labels
}
