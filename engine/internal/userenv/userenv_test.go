package userenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The answer is fenced by markers because a profile is allowed to print; what
// it prints is not the answer and must not be read as one.
func TestBetweenTakesWhatTheMarkersFence(t *testing.T) {
	s := "welcome back\n" + marker + "/opt/homebrew/bin:/usr/bin" + marker + "\n"
	got, ok := between(s, marker)
	if !ok || got != "/opt/homebrew/bin:/usr/bin" {
		t.Errorf("between = %q, %v", got, ok)
	}
	if _, ok := between("no markers here", marker); ok {
		t.Error("an answer without markers should not be read as one")
	}
	if _, ok := between(marker+"only one", marker); ok {
		t.Error("one marker is half an answer")
	}
}

func TestMergePutsTheLoginShellFirstAndKeepsWhatIsThere(t *testing.T) {
	got := Merge("/opt/homebrew/bin:/usr/bin", "/usr/bin:/usr/sbin:/opt/homebrew/bin")
	want := "/opt/homebrew/bin:/usr/bin:/usr/sbin"
	if got != want {
		t.Errorf("Merge = %q, want %q", got, want)
	}
	// A daemon started from a terminal already has the right PATH: its own
	// order is kept, and nothing is appended twice.
	if got := Merge("/usr/bin:/bin", "/usr/bin:/bin"); got != "/usr/bin:/bin" {
		t.Errorf("Merge of the same list = %q", got)
	}
	// Empty entries — a leading or trailing colon — are not directories.
	if got := Merge(":/usr/bin:", "::/bin::"); got != "/usr/bin:/bin" {
		t.Errorf("Merge with blanks = %q", got)
	}
	if got := Merge("", "/usr/bin"); got != "/usr/bin" {
		t.Errorf("Merge with no answer = %q, want the current PATH alone", got)
	}
}

// LoginPath runs whatever $SHELL names, so a test can hand it something that
// answers like a shell without being the user's.
func TestLoginPathReadsWhatTheShellPrints(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	path, ok := LoginPath(10 * time.Second)
	if !ok {
		t.Skip("no answer from /bin/sh in this environment")
	}
	if !strings.Contains(path, "/bin") {
		t.Errorf("path = %q, want the shell's own PATH", path)
	}
}

func TestLoginPathSkipsAShellThatIsNotThere(t *testing.T) {
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "not-a-shell"))
	if _, ok := LoginPath(time.Second); ok {
		t.Error("a missing shell should answer nothing")
	}
}

// A profile that hangs must not take the daemon with it — the daemon comes up
// with the PATH it already had.
func TestLoginPathGivesUpOnASlowShell(t *testing.T) {
	slow := filepath.Join(t.TempDir(), "slow-shell")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", slow)

	start := time.Now()
	if _, ok := LoginPath(200 * time.Millisecond); ok {
		t.Error("a shell that never answers should not produce a path")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("waited %s, want the timeout respected", waited)
	}
}

// A shell whose answer has no markers — a wrapper that swallows the command —
// is no answer.
func TestLoginPathRejectsAnAnswerWithoutMarkers(t *testing.T) {
	noisy := filepath.Join(t.TempDir(), "noisy-shell")
	if err := os.WriteFile(noisy, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", noisy)
	if _, ok := LoginPath(5 * time.Second); ok {
		t.Error("output without markers is not a PATH")
	}
}
