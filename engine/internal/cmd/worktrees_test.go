package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

func withWorktreeFlags(t *testing.T, jsonOut, gone bool) {
	t.Helper()
	prevJSON, prevGone := worktreesJSONFlag, worktreesGoneFlag
	worktreesJSONFlag, worktreesGoneFlag = jsonOut, gone
	t.Cleanup(func() { worktreesJSONFlag, worktreesGoneFlag = prevJSON, prevGone })
}

func TestWorktreesListsPresentAndGoneCheckouts(t *testing.T) {
	db := filepath.Join(t.TempDir(), "option-berth.db")
	t.Setenv("BERTH_DB", db)
	present := t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted")

	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []store.WorktreeIdentity{
		{Identity: "wt_0123456789ab", Root: present, Group: "demo@feat"},
		{Identity: "wt_abcdef012345", Root: gone, Group: "demo@old"},
	} {
		if err := st.RecordWorktreeIdentity(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	withWorktreeFlags(t, true, false)
	var decode worktreesDocument
	out := captureStdout(t, func() {
		if err := worktreesRun(worktreesCmd, nil); err != nil {
			t.Fatalf("worktreesRun: %v", err)
		}
	})
	if err := json.Unmarshal([]byte(out), &decode); err != nil {
		t.Fatalf("stdout is not the worktrees document: %v\n%s", err, out)
	}
	if len(decode.Worktrees) != 2 {
		t.Fatalf("worktrees = %+v, want both identities", decode.Worktrees)
	}
	byIdentity := map[string]worktreeIdentityRow{}
	for _, row := range decode.Worktrees {
		byIdentity[row.Identity] = row
	}
	if !byIdentity["wt_0123456789ab"].Exists {
		t.Error("the live checkout is reported gone")
	}
	if byIdentity["wt_abcdef012345"].Exists {
		t.Error("the deleted checkout is reported present")
	}

	withWorktreeFlags(t, true, true)
	out = captureStdout(t, func() {
		if err := worktreesRun(worktreesCmd, nil); err != nil {
			t.Fatalf("worktreesRun --gone: %v", err)
		}
	})
	if err := json.Unmarshal([]byte(out), &decode); err != nil {
		t.Fatal(err)
	}
	if len(decode.Worktrees) != 1 || decode.Worktrees[0].Identity != "wt_abcdef012345" {
		t.Fatalf("--gone = %+v, want only the deleted checkout", decode.Worktrees)
	}
}

func TestWorktreesWithoutADatabaseIsAnEmptyList(t *testing.T) {
	t.Setenv("BERTH_DB", filepath.Join(t.TempDir(), "absent.db"))
	withWorktreeFlags(t, true, false)
	out := captureStdout(t, func() {
		if err := worktreesRun(worktreesCmd, nil); err != nil {
			t.Fatalf("worktreesRun: %v", err)
		}
	})
	var doc worktreesDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if len(doc.Worktrees) != 0 {
		t.Fatalf("worktrees = %+v, want none", doc.Worktrees)
	}
}

func TestWorktreesReadsWithoutCreatingADatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "absent.db")
	t.Setenv("BERTH_DB", db)
	withWorktreeFlags(t, true, false)
	_ = captureStdout(t, func() {
		_ = worktreesRun(worktreesCmd, nil)
	})
	if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database was created by a read-only command: %v", err)
	}
}
