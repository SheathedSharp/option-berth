package runtimecheck

import (
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeStoreReopenPreservesClaimsExitAndGoneIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = st.AppendRunExit(store.RunExitRow{ID: "finished", PID: 42, Group: "a", Name: "worker", StartedAt: now.Add(-time.Minute), ExitedAt: now, Reason: "crashed", LogPath: "/synthetic/historical.log", LogOffset: 77}); err != nil {
		t.Fatal(err)
	}
	if err = st.Claims().Put(store.ClaimRow{Port: 17001, Key: "a/service", Project: "a", Worktree: "gone", Since: now, Until: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err = st.RecordWorktreeIdentity(store.WorktreeIdentity{Identity: "wt_0123456789ab", Root: filepath.Join(t.TempDir(), "deleted"), Group: "a", Project: "a", Worktree: "gone"}); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	// Upgrade/downgrade keeps the exact existing format; the read-only path
	// must not create a second writer or require the checkout to exist.
	reopened, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows, err := reopened.RunExits(10)
	if err != nil || len(rows) != 1 || rows[0].LogOffset != 77 || rows[0].ID != "finished" {
		t.Fatal(rows, err)
	}
	claims, err := reopened.Claims().List()
	if err != nil || len(claims) != 1 || claims[0].Port != 17001 {
		t.Fatal(claims, err)
	}
	identity, found, err := reopened.WorktreeIdentity("wt_0123456789ab")
	if err != nil || !found || identity.Worktree != "gone" {
		t.Fatal(identity, found, err)
	}
	if err = reopened.AppendRunExit(store.RunExitRow{ID: "forbidden"}); err == nil {
		t.Fatal("read-only fallback became writer")
	}
}
func TestRuntimeServiceLockHandoffUsesSameStablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-service.lock")
	first, err := runtimesvc.AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if second, err := runtimesvc.AcquireLock(path); err == nil {
		second.Release()
		t.Fatal("two runtime service writers admitted")
	}
	if err = first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := runtimesvc.AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if _, err = os.Stat(path); err != nil {
		t.Fatal("handoff removed lock inode", err)
	}
}
