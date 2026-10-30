package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWorktreeIdentitiesKeepFirstSeenAndTrackGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	root := filepath.Join(t.TempDir(), "checkout")
	first := time.Now().Add(-time.Hour)
	rec := WorktreeIdentity{
		Identity: "wt_0123456789ab", Root: root, Group: "demo@feat",
		ConfigPath: filepath.Join(root, "oberth.yaml"),
		Project:    "demo", Worktree: "feat", FirstSeen: first,
	}
	if err := st.RecordWorktreeIdentity(rec); err != nil {
		t.Fatal(err)
	}
	// A later observation with a renamed group keeps the original first_seen.
	rec.Group = "store@feat"
	if err := st.RecordWorktreeIdentity(rec); err != nil {
		t.Fatal(err)
	}

	got, ok, err := st.WorktreeIdentity("wt_0123456789ab")
	if err != nil || !ok {
		t.Fatalf("WorktreeIdentity = %+v, %v, %v", got, ok, err)
	}
	if got.Group != "store@feat" {
		t.Errorf("group = %q, want the latest name", got.Group)
	}
	if !got.FirstSeen.Equal(first.Truncate(time.Nanosecond)) {
		t.Errorf("first_seen = %v, want %v", got.FirstSeen, first)
	}
	if got.ConfigPath != rec.ConfigPath || got.Project != "demo" || got.Worktree != "feat" {
		t.Errorf("record = %+v", got)
	}

	byRoot, ok, err := st.WorktreeIdentityByRoot(root)
	if err != nil || !ok || byRoot.Identity != rec.Identity {
		t.Fatalf("WorktreeIdentityByRoot = %+v, %v, %v", byRoot, ok, err)
	}
	group, err := st.WorktreeIdentityGroup("store@feat")
	if err != nil || len(group) != 1 || group[0].Identity != rec.Identity {
		t.Fatalf("WorktreeIdentityGroup = %+v, %v", group, err)
	}
}

func TestWorktreeIdentitySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	root := filepath.Join(t.TempDir(), "gone")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RecordWorktreeIdentity(WorktreeIdentity{
		Identity: "wt_abcdef012345", Root: root, Group: "demo",
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// A reader can open the ledger without creating or migrating anything.
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	rows, err := ro.WorktreeIdentities()
	if err != nil {
		t.Fatal(err)
	}
	want, err := cleanRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Root != want {
		t.Fatalf("rows = %+v, want root %q", rows, want)
	}
}

func TestWorktreeIdentityRejectsAnEmptyToken(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.RecordWorktreeIdentity(WorktreeIdentity{Root: "/tmp/x"}); err == nil {
		t.Fatal("an identity without a token was accepted")
	}
}
