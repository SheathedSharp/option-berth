package runtimesvc

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeLog writes content to a fresh file and returns its path.
func writeLog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

// appendLog adds content to an existing file.
func appendLog(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("opening the fixture for append: %v", err)
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		t.Fatalf("appending: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
}

func TestTailFileReturnsTheLastLinesOldestFirst(t *testing.T) {
	path := writeLog(t, "one\ntwo\nthree\nfour\n")
	got, err := tailFile(path, 2)
	if err != nil {
		t.Fatalf("tailFile: %v", err)
	}
	if want := []string{"three", "four"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// Asking for more lines than the file holds is not an error: the caller gets
// what there is.
func TestTailFileReturnsWhatThereIs(t *testing.T) {
	path := writeLog(t, "only\n")
	got, err := tailFile(path, 100)
	if err != nil {
		t.Fatalf("tailFile: %v", err)
	}
	if want := []string{"only"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// A log whose last write did not end in a newline still has that line: dropping
// it would hide the very line a reader is looking for.
func TestTailFileKeepsAnUnterminatedLastLine(t *testing.T) {
	path := writeLog(t, "started\nlistening on :3000")
	got, err := tailFile(path, 2)
	if err != nil {
		t.Fatalf("tailFile: %v", err)
	}
	if want := []string{"started", "listening on :3000"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

func TestTailFileOnAnEmptyFile(t *testing.T) {
	got, err := tailFile(writeLog(t, ""), 10)
	if err != nil {
		t.Fatalf("tailFile: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("lines = %v, want none", got)
	}
}

func TestTailFileOnAMissingFile(t *testing.T) {
	if _, err := tailFile(filepath.Join(t.TempDir(), "gone.log"), 10); err == nil {
		t.Fatal("a missing file was read without an error")
	}
}

// The window grows until it holds the requested lines, so a file far larger than
// one read still answers exactly — including when the last lines are long.
func TestTailFileReadsTheEndOfALargeFile(t *testing.T) {
	var builder strings.Builder
	for i := range 20000 {
		builder.WriteString(strings.Repeat("x", 100))
		builder.WriteString("\n")
		if i%1000 == 0 {
			builder.WriteString("marker-" + time.Duration(i).String() + "\n")
		}
	}
	builder.WriteString("final\n")
	path := writeLog(t, builder.String())

	got, err := tailFile(path, 2)
	if err != nil {
		t.Fatalf("tailFile: %v", err)
	}
	if want := []string{strings.Repeat("x", 100), "final"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q, want the last two", got)
	}
}

// Following emits the backlog first, then every line appended afterwards, and
// stops when the context does.
func TestFollowFilesEmitsBacklogThenAppendedLines(t *testing.T) {
	path := writeLog(t, "first\nsecond\n")
	previous := followPollInterval
	followPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { followPollInterval = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []string
	err := followFiles(ctx, []string{path}, 2, func(line string) error {
		got = append(got, line)
		if len(got) == 2 {
			appendLog(t, path, "third\n")
		}
		if len(got) == 3 {
			cancel()
		}
		return nil
	})
	if err == nil {
		t.Fatal("following returned no error after its context ended")
	}
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// A half-written line is held until its newline arrives: a reader must never see
// a fragment presented as a line.
func TestFollowFilesHoldsAPartialLine(t *testing.T) {
	path := writeLog(t, "complete\n")
	previous := followPollInterval
	followPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { followPollInterval = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []string
	err := followFiles(ctx, []string{path}, 1, func(line string) error {
		got = append(got, line)
		if len(got) == 1 {
			// Half a line now, the rest after a few polls: nothing may be
			// emitted until the newline arrives.
			appendLog(t, path, "half")
			time.AfterFunc(40*time.Millisecond, func() { appendLog(t, path, "-done\n") })
		}
		if len(got) == 2 {
			cancel()
		}
		return nil
	})
	if err == nil {
		t.Fatal("following returned no error after its context ended")
	}
	if want := []string{"complete", "half-done"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// A file that shrinks was truncated or replaced; reading restarts rather than
// waiting for it to grow back past the old offset.
func TestFollowFilesRestartsAfterTruncation(t *testing.T) {
	path := writeLog(t, "before\n")
	previous := followPollInterval
	followPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { followPollInterval = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []string
	err := followFiles(ctx, []string{path}, 1, func(line string) error {
		got = append(got, line)
		if len(got) == 1 {
			if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
				t.Fatalf("truncating: %v", err)
			}
		}
		if len(got) == 2 {
			cancel()
		}
		return nil
	})
	if err == nil {
		t.Fatal("following returned no error after its context ended")
	}
	if want := []string{"before", "after"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// Following a file that does not exist is a read failure, not a silent wait: the
// caller asked for output that is not there.
func TestFollowFilesOnAMissingFileFails(t *testing.T) {
	err := followFiles(context.Background(), []string{filepath.Join(t.TempDir(), "gone.log")}, 1,
		func(string) error { return nil })
	if err == nil {
		t.Fatal("following a missing file succeeded")
	}
}

// A service's log is read without running a program: the answer is the file's
// own content, and the label says which file it came from.
func TestAFileSourceReadsItsLogWithoutAProgram(t *testing.T) {
	first := writeLog(t, "one\ntwo\n")
	second := filepath.Join(filepath.Dir(first), "second.log")
	if err := os.WriteFile(second, []byte("three\n"), 0o600); err != nil {
		t.Fatalf("writing the second fixture: %v", err)
	}

	source := fileLog{paths: []string{first, second}, labels: []string{"a.log", "b.log"}}
	lines, err := source.Tail(context.Background(), 5)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if want := []string{"one", "two", "three"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	if got, want := source.Label(), "a.log, b.log"; got != want {
		t.Errorf("Label() = %q, want %q", got, want)
	}
}

// A file that disappeared between the scan and the read is reported, not hidden:
// the caller asked for output and there is none to give.
func TestAFileSourceReportsAMissingFile(t *testing.T) {
	source := fileLog{paths: []string{filepath.Join(t.TempDir(), "gone.log")}, labels: []string{"gone.log"}}
	if _, err := source.Tail(context.Background(), 5); err == nil {
		t.Fatal("a missing log file was read without an error")
	}
}
