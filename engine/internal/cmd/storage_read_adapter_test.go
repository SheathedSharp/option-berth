package cmd

import (
	"context"
	"errors"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"path/filepath"
	"testing"
)

func TestStoredReadUsesOwnerAndRejectsSilentFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.OpenOwned(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.RecordWorktreeIdentity(store.WorktreeIdentity{Identity: "wt_fixture", Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	owner := func(ctx context.Context, section string) (rpc.RuntimeStorageReadResult, bool, error) {
		rows, err := db.WorktreeIdentities()
		if err != nil {
			return rpc.RuntimeStorageReadResult{}, true, err
		}
		out := make([]rpc.RuntimeStorageWorktree, 0, len(rows))
		for _, r := range rows {
			out = append(out, rpc.RuntimeStorageWorktree(r))
		}
		return rpc.RuntimeStorageReadResult{Section: section, Worktrees: &out}, true, nil
	}
	decode := func(r rpc.RuntimeStorageReadResult) ([]store.WorktreeIdentity, error) {
		if r.Worktrees == nil {
			return nil, errors.New("missing worktrees receipt")
		}
		out := []store.WorktreeIdentity{}
		for _, row := range *r.Worktrees {
			out = append(out, store.WorktreeIdentity(row))
		}
		return out, nil
	}
	local := func(db *store.Store) ([]store.WorktreeIdentity, error) { return db.WorktreeIdentities() }
	rows, err := storedRead(context.Background(), path, "worktrees", false, owner, decode, local)
	if err != nil || len(rows) != 1 {
		t.Fatalf("online owner read: %v, %v", rows, err)
	}
	_, err = storedRead(context.Background(), path, "worktrees", true, owner, decode, local)
	if !errors.Is(err, store.ErrStorageOwned) {
		t.Fatalf("direct read must explicitly refuse an owned DB: %v", err)
	}
	sentinel := errors.New("authoritative owner failure")
	bad := func(context.Context, string) (rpc.RuntimeStorageReadResult, bool, error) {
		return rpc.RuntimeStorageReadResult{}, true, sentinel
	}
	_, err = storedRead(context.Background(), path, "worktrees", false, bad, decode, local)
	if !errors.Is(err, sentinel) {
		t.Fatalf("lost owner failure: %v", err)
	}
	wrong := func(context.Context, string) (rpc.RuntimeStorageReadResult, bool, error) {
		return rpc.RuntimeStorageReadResult{Section: "schema"}, true, nil
	}
	if _, err := storedRead(context.Background(), path, "worktrees", false, wrong, decode, local); err == nil {
		t.Fatal("accepted wrong read section")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	absent := func(context.Context, string) (rpc.RuntimeStorageReadResult, bool, error) {
		return rpc.RuntimeStorageReadResult{}, false, errors.New("no listener")
	}
	rows, err = storedRead(context.Background(), path, "worktrees", false, absent, decode, local)
	if err != nil || len(rows) != 1 {
		t.Fatalf("offline evidence must survive: %v, %v", rows, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = storedRead(ctx, path, "worktrees", false, owner, decode, local)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}
