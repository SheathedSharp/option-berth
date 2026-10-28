//go:build integration

package daemon_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The workplace record end to end, against the real binary and a real daemon.
//
// The unit tests cover each piece — the CLI records a run, the registry
// announces its exit, the workplace closes the block that run opened. This is
// the one that says the three are wired to each other: a detached run is the
// case where no single process sees both ends of the work, so it is the case
// where a broken wire would go unnoticed.

// workplaceRepo is a git checkout for the commands to run in. A workplace
// belongs to a worktree, and the CLI resolves one from the directory it is run
// in.
func workplaceRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "--allow-empty", "-m", "first"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

// in runs one CLI line in a directory and returns its combined output.
func (e *env) in(dir string, args ...string) (string, error) {
	e.t.Helper()
	cmd := e.command(args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// workplaceShow reads the worktree's active workplace.
func (e *env) workplaceShow(dir string) workplaceShowDoc {
	e.t.Helper()
	out, err := e.in(dir, "workplace", "show", "--json")
	if err != nil {
		e.t.Fatalf("workplace show: %v\n%s", err, out)
	}
	var doc workplaceShowDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		e.t.Fatalf("workplace show is not the documented document: %v\n%s", err, out)
	}
	return doc
}

type workplaceShowDoc struct {
	Workplace struct {
		Name   string `json:"name"`
		Active bool   `json:"active"`
		Blocks int    `json:"blocks"`
	} `json:"workplace"`
	Blocks []struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Source   string `json:"source"`
		Input    string `json:"input"`
		Status   string `json:"status"`
		RunID    string `json:"run_id"`
		PID      int    `json:"pid"`
		LogPath  string `json:"log_path"`
		ExitCode *int   `json:"exit_code"`
		EndedAt  string `json:"ended_at"`
	} `json:"blocks"`
}

func TestDetachedRunIsRecordedAndClosedByTheDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the detach harness is unix-shaped: it needs a socket and /bin/echo")
	}
	if _, err := os.Stat("/bin/echo"); err != nil {
		t.Skip("/bin/echo is not available")
	}
	e := newEnv(t)
	e.serve()
	repo := workplaceRepo(t)

	// A command the daemon owns: this process hands it over and returns, so it
	// is the daemon that will learn how it ended.
	if out, err := e.in(repo, "start", "-d", "--", "/bin/echo", "hello"); err != nil {
		t.Fatalf("start -d: %v\n%s", err, out)
	}

	// The run is recorded before the command returns, and starts out open:
	// nothing has observed it end yet.
	doc := e.workplaceShow(repo)
	if len(doc.Blocks) != 1 {
		t.Fatalf("a detached run recorded %d blocks: %+v", len(doc.Blocks), doc.Blocks)
	}
	block := doc.Blocks[0]
	if block.Kind != "shell" || block.Source != "engine" {
		t.Fatalf("the block is not an engine shell block: %+v", block)
	}
	if block.Input != "/bin/echo hello" {
		t.Fatalf("input = %q, want the command line", block.Input)
	}
	if block.RunID == "" || block.PID == 0 || block.LogPath == "" {
		t.Fatalf("the block does not point at the run it recorded: %+v", block)
	}
	if _, err := os.Stat(filepath.Clean(block.LogPath)); err != nil {
		t.Fatalf("the log the block points at is not there: %v", err)
	}

	// Then the run ends, and the daemon closes the block. Nothing in this test
	// observes the exit: the daemon is the only thing that can.
	deadline := time.Now().Add(20 * time.Second)
	for {
		doc = e.workplaceShow(repo)
		if len(doc.Blocks) == 1 && doc.Blocks[0].Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never closed the block: %+v", doc.Blocks)
		}
		time.Sleep(200 * time.Millisecond)
	}
	closed := doc.Blocks[0]
	if closed.Status != "completed" {
		t.Fatalf("status = %q, want completed", closed.Status)
	}
	if closed.ExitCode == nil || *closed.ExitCode != 0 {
		t.Fatalf("the block has no exit code: %+v", closed)
	}
	if closed.EndedAt == "" {
		t.Fatalf("the closed block has no end time: %+v", closed)
	}
	if closed.Input != "/bin/echo hello" || closed.RunID != block.RunID {
		t.Fatalf("closing the block rewrote what it recorded: %+v", closed)
	}

	// A workplace nobody named is still a workplace the person can read back,
	// and it is the one new work lands in.
	if doc.Workplace.Name == "" || !doc.Workplace.Active || doc.Workplace.Blocks != 1 {
		t.Fatalf("workplace = %+v", doc.Workplace)
	}
}
