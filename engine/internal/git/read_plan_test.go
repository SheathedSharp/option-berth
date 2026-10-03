package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// Each command waits for the other to start. A sequential implementation
// cannot pass this barrier; no elapsed-time speedup threshold is involved.
func TestReadRestOverlapsIndependentCommands(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dir := groups.Canonical(t.TempDir())
	snap := Snapshot{Root: dir, Head: strings.Repeat("a", 40)}
	worktreeStarted, logStarted := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	read := func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		calls.Add(1)
		switch args[0] {
		case "worktree":
			close(worktreeStarted)
			select {
			case <-logStarted:
				return []byte("worktree " + dir + "\x00HEAD " + snap.Head + "\x00\x00"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case "log":
			if !slices.Contains(args, snap.Head) || args[len(args)-1] != "--" {
				return nil, fmt.Errorf("log did not pin the observed commit: %v", args)
			}
			select {
			case <-worktreeStarted:
				close(logStarted)
				return []byte("aaaaaaa\x00first\x002026-01-01T00:00:00Z\n"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, fmt.Errorf("unexpected command %v", args)
	}
	if err := readRestWith(ctx, dir, &snap, read); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || snap.Last == nil || snap.Last.Subject != "first" || len(snap.Worktrees) != 1 || !snap.Worktrees[0].Current {
		t.Fatalf("incomplete metadata: calls=%d snapshot=%+v", calls.Load(), snap)
	}
}

func TestReadRestUnbornSkipsLog(t *testing.T) {
	var calls atomic.Int32
	snap := Snapshot{}
	err := readRestWith(context.Background(), "", &snap, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls.Add(1)
		if args[0] != "worktree" {
			return nil, fmt.Errorf("unborn branch launched %v", args)
		}
		return nil, nil
	})
	if err != nil || calls.Load() != 1 || snap.Last != nil {
		t.Fatalf("calls=%d last=%+v err=%v", calls.Load(), snap.Last, err)
	}
}

func TestReadRestFailureCancelsAndJoinsSibling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := make(chan struct{})
	failure := errors.New("worktree listing failed")
	var joined atomic.Bool
	snap := Snapshot{Head: strings.Repeat("b", 40), Last: &Commit{Subject: "untouched"}}
	err := readRestWith(ctx, "", &snap, func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "worktree" {
			select {
			case <-started:
				return nil, failure
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		close(started)
		<-ctx.Done()
		joined.Store(true)
		return nil, ctx.Err()
	})
	if !errors.Is(err, failure) || !joined.Load() {
		t.Fatalf("failure=%v sibling joined=%v", err, joined.Load())
	}
	if snap.Last.Subject != "untouched" || snap.Worktrees != nil {
		t.Fatal("failed read partially replaced the caller's metadata")
	}
}

func TestReadRestCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	err := readRestWith(ctx, "", &Snapshot{}, func(context.Context, string, ...string) ([]byte, error) {
		calls.Add(1)
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestReadRestOptionalLogFailureDoesNotDropWorktrees(t *testing.T) {
	snap := Snapshot{Head: strings.Repeat("c", 40)}
	err := readRestWith(context.Background(), "", &snap, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "log" {
			return nil, errors.New("optional commit metadata unavailable")
		}
		return []byte("worktree /fixture\x00\x00"), nil
	})
	if err != nil || snap.Last != nil || len(snap.Worktrees) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snap, err)
	}
}

func TestCountsOnlyUntrackedNeverLaunchesGit(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "new.txt", "one\ntwo")
	write(t, dir, "binary", "\x00data")
	files := []File{{Path: "new.txt", Status: "??"}, {Path: "binary", Status: "??"}}
	calls := 0
	err := addCountsWith(context.Background(), dir, dir, "", files, func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("unneeded diff")
	})
	if err != nil || calls != 0 || files[0].Additions == nil || *files[0].Additions != 2 || files[0].Deletions == nil || *files[0].Deletions != 0 || !files[1].Binary {
		t.Fatalf("calls=%d files=%+v err=%v", calls, files, err)
	}
}

func TestCountsUsesOnePinnedDiffForMixedRows(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "new.txt", "new\n")
	for _, head := range []string{"", strings.Repeat("d", 40)} {
		files := []File{{Path: "a.txt", Status: " M"}, {Path: "b.txt", Status: "M "}, {Path: "new.txt", Status: "??"}}
		calls := 0
		err := addCountsWith(context.Background(), dir, dir, head, files, func(_ context.Context, _ string, args ...string) ([]byte, error) {
			calls++
			if args[len(args)-1] != "--" || slices.Contains(args, "HEAD") || (head != "" && !slices.Contains(args, head)) || (head == "" && !slices.Contains(args, "--cached")) {
				return nil, fmt.Errorf("wrong diff baseline: %v", args)
			}
			return []byte("2\t1\ta.txt\x001\t0\tb.txt\x00"), nil
		})
		if err != nil || calls != 1 || files[0].Additions == nil || *files[0].Additions != 2 || files[2].Additions == nil || *files[2].Additions != 1 {
			t.Fatalf("head=%q calls=%d files=%+v err=%v", head, calls, files, err)
		}
	}
}

func TestCountsCancelledDoesNoFurtherWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := addCountsWith(ctx, "", "", "", []File{{Path: "unused", Status: " M"}}, func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// This is a real repository transition between commands, not a fake hash.
func TestReadMetadataAndCountsKeepObservedCommit(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	run(t, dir, "config", "core.autocrlf", "false")
	write(t, dir, "a.txt", "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	write(t, dir, "a.txt", "one\nworking\n")
	snap, files, err := readStatus(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	snap.Root = dir
	write(t, dir, "a.txt", "one\ntwo\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "second")
	write(t, dir, "a.txt", "one\ntwo\nthree\n")
	if err := readRest(context.Background(), dir, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Last == nil || snap.Last.Subject != "first" || !strings.HasPrefix(snap.Head, snap.Last.Hash) {
		t.Fatalf("last commit drifted to later HEAD: %+v", snap.Last)
	}
	if err := addCounts(context.Background(), dir, dir, snap.Head, files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Additions == nil || *files[0].Additions != 2 || files[0].Deletions == nil || *files[0].Deletions != 0 {
		t.Fatalf("counts did not use the captured commit: %+v", files)
	}
}

func TestReadTreeDoesNotRefreshRepositoryIndex(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "a.txt", "unchanged\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	index := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	// Change only file metadata: ordinary git status would refresh the cached
	// stat record in the index even though there is no user-visible change.
	at := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "a.txt"), at, at); err != nil {
		t.Fatal(err)
	}
	tree, err := ReadTree(context.Background(), dir)
	if err != nil || !tree.Clean() {
		t.Fatalf("tree=%+v err=%v", tree, err)
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	now, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !info.ModTime().Equal(now.ModTime()) {
		t.Fatal("a read-only observation rewrote the repository index")
	}
}
