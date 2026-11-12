//go:build integration

// The acceptance path for git-root grouping: a listener started inside a git
// checkout is grouped under the repository, on every platform, with no
// `oberth.yaml` anywhere near it.
//
// Nothing here is guarded by OS, which is the whole point. Before the PEB
// reader landed `batchGetCwds` was a no-op on Windows: every row there had an
// empty cwd, no project_root and no git-root group, and no test said so, since
// the ones that could have were written to skip. Run with
// `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// TestCwdGroupsPortsUnderTheGitRoot is the demo: two listeners started inside
// one checkout — one at its root, one in a subdirectory — carry the working
// directory they were started in, resolve a project_root at the checkout, and
// land in the same `<repo>` group in the daemon snapshot without any config
// file claiming them.
func TestCwdGroupsPortsUnderTheGitRoot(t *testing.T) {
	helper, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	sandbox := newEnv(t)

	// A checkout is nothing more than a directory holding a `.git` entry —
	// groups.Find only ever Lstats it — so no git binary is needed here.
	repoName := "option-berth-cwd-demo"
	repo := filepath.Join(sandbox.home, repoName)
	checkout := filepath.Join(repo, ".git")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("making the checkout marker %s: %v", checkout, err)
	}
	nested := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("making the nested directory %s: %v", nested, err)
	}
	// Deliberately no oberth.yaml: the group under test is the git-root one.
	if _, err := os.Stat(filepath.Join(repo, groups.ConfigName)); err == nil {
		t.Fatalf("%s exists; this test is about grouping without one", groups.ConfigName)
	}

	sandbox.serve()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cl := sandbox.connect(ctx)
	// A subscriber keeps the daemon's scan loop running: with nobody listening
	// it parks between requests.
	sub, err := cl.Subscribe(ctx, client.SubscribeOptions{Buffer: 256})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}
	go drain(ctx, sub)

	rootPort, nestedPort := unusedPort(t), unusedPort(t)
	startListener(t, helper, repo, rootPort)
	startListener(t, helper, nested, nestedPort)

	wantCwd := map[int]string{
		rootPort:   groups.Canonical(repo),
		nestedPort: groups.Canonical(nested),
	}
	wantRoot := groups.Canonical(repo)

	rows := waitForCwds(t, ctx, cl, wantCwd, 90*time.Second)

	for port, want := range wantCwd {
		row := rows[port]
		if row.Cwd == "" {
			t.Errorf("port %d has no cwd; the scanner cannot read this platform's per-process working directory", port)
			continue
		}
		// The raw field is compared canonically because each platform spells
		// one directory its own way: /private/var on macOS, the long path on
		// Windows, the resolved symlink on Linux.
		if got := groups.Canonical(row.Cwd); got != want {
			t.Errorf("port %d cwd = %q (canonical %q), want %q", port, row.Cwd, got, want)
		}
		// Spellings the rest of option-berth must never see, on any platform.
		if strings.HasPrefix(row.Cwd, `\\?\`) {
			t.Errorf("port %d cwd = %q keeps the extended-length prefix", port, row.Cwd)
		}
		if len(row.Cwd) > 1 && strings.HasSuffix(row.Cwd, string(filepath.Separator)) {
			t.Errorf("port %d cwd = %q keeps a trailing separator", port, row.Cwd)
		}

		if row.ProjectRoot == nil || *row.ProjectRoot == "" {
			t.Errorf("port %d has no project_root even though its cwd is %q", port, row.Cwd)
		} else if got := groups.Canonical(*row.ProjectRoot); got != wantRoot {
			t.Errorf("port %d project_root = %q (canonical %q), want the checkout %q",
				port, *row.ProjectRoot, got, wantRoot)
		}

		if row.Group == nil || *row.Group != repoName {
			t.Errorf("port %d group = %v, want %q from the git root", port, deref(row.Group), repoName)
		}
		if row.GroupSource == nil || *row.GroupSource != state.SourceAuto {
			t.Errorf("port %d group_source = %v, want %q (no oberth.yaml claims it)",
				port, deref((*string)(row.GroupSource)), state.SourceAuto)
		}
	}
}

// startListener spawns the helper inside dir and registers its cleanup. dir,
// rather than the test's own working directory, is the cwd the scanner has to
// read back.
func startListener(t *testing.T, helper, dir string, port int) {
	t.Helper()
	run := exec.Command(helper, strconv.Itoa(port))
	run.Dir = dir
	run.WaitDelay = waitDelay
	var log safeBuffer
	run.Stdout, run.Stderr = &log, &log
	ownProcessGroup(run)
	if err := run.Start(); err != nil {
		t.Fatalf("starting the listener in %s: %v", dir, err)
	}
	t.Cleanup(func() {
		stopCommand(run)
		if t.Failed() && log.String() != "" {
			t.Logf("listener on %d:\n%s", port, log.String())
		}
	})
	limit := time.Now().Add(30 * time.Second)
	for time.Now().Before(limit) {
		if portOpen(port) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the listener never came up on %d:\n%s", port, log.String())
}

// waitForCwds polls state.snapshot until every wanted port is present with a
// non-empty cwd, then returns the rows. The scan that first sees a port can
// race the process's own startup, so a row without a cwd is retried rather than
// failed on; the timeout is the real assertion.
func waitForCwds(t *testing.T, ctx context.Context, cl *client.Client, want map[int]string, timeout time.Duration) map[int]state.Port {
	t.Helper()
	rows := map[int]state.Port{}
	limit := time.Now().Add(timeout)
	for time.Now().Before(limit) {
		var snapshot state.Snapshot
		if err := cl.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snapshot); err != nil {
			t.Fatalf("state.snapshot: %v", err)
		}
		withCwd := 0
		for _, row := range snapshot.Ports {
			if _, wanted := want[row.Port]; !wanted {
				continue
			}
			rows[row.Port] = row
			if row.Cwd != "" {
				withCwd++
			}
		}
		if withCwd == len(want) {
			return rows
		}
		time.Sleep(500 * time.Millisecond)
	}
	for port := range want {
		row, seen := rows[port]
		if !seen {
			t.Errorf("the daemon never reported port %d at all", port)
			continue
		}
		t.Errorf("port %d never got a cwd; last row: %+v", port, row)
	}
	t.Fatalf("the spawned listeners never reported a cwd within %s", timeout)
	return rows
}

// drain keeps a subscription alive: a subscriber that stops reading is dropped,
// and the scan loop parks again with it.
func drain(ctx context.Context, sub *client.Subscription) {
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case _, open := <-sub.Deltas:
			if !open {
				return
			}
		case _, open := <-sub.Events:
			if !open {
				return
			}
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
