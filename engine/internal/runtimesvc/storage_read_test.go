package runtimesvc

import (
	"context"
	"errors"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageReadUsesOwningProductionStore(t *testing.T) {
	db, err := store.OpenOwned(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.RecordWorktreeIdentity(store.WorktreeIdentity{Identity: "wt_fixture", Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendRunExit(store.RunExitRow{ID: "fixture-exit", Group: "fixture", Name: "worker", Code: 42, Reason: "failed", ExitedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"schema", "worktrees", "run_exits"} {
		got, err := readStorage(context.Background(), db, rpc.RuntimeStorageReadParams{Section: section})
		if err != nil {
			t.Fatal(err)
		}
		if got.Section != section {
			t.Fatalf("section=%q", got.Section)
		}
		switch section {
		case "schema":
			if got.Schema == nil || got.Schema.Version != store.LatestVersion() {
				t.Fatalf("schema=%+v", got.Schema)
			}
		case "worktrees":
			if got.Worktrees == nil || len(*got.Worktrees) != 1 || (*got.Worktrees)[0].Identity != "wt_fixture" {
				t.Fatalf("worktrees=%+v", got.Worktrees)
			}
		case "run_exits":
			if got.RunExits == nil || len(*got.RunExits) != 1 || (*got.RunExits)[0].Code != 42 {
				t.Fatalf("exits=%+v", got.RunExits)
			}
		}
	}
	if _, err := readStorage(context.Background(), db, rpc.RuntimeStorageReadParams{Section: "sql"}); err == nil {
		t.Fatal("accepted arbitrary SQL section")
	}
	conn, err := db.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = readStorage(ctx, db, rpc.RuntimeStorageReadParams{Section: "schema"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read waiting for owned connection ignored cancellation: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readStorage(context.Background(), db, rpc.RuntimeStorageReadParams{Section: "schema"}); err != nil {
		t.Fatalf("cancelled reader leaked connection: %v", err)
	}
}
