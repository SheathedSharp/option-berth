package runsreg

import "testing"

// TestRenameGroupsMovesRunsWithTheProject: a service started before its
// project was renamed follows the rename instead of keeping a group of the old
// name to itself, while a run in an unrelated group is left where it is. An
// empty rename is a no-op that reports nothing moved.
func TestRenameGroupsMovesRunsWithTheProject(t *testing.T) {
	reg := New()
	reg.Mirror = false
	for _, rec := range []Record{
		{PID: 611, Group: "shop", Name: "api"},
		{PID: 612, Group: "shop@feat", Name: "api"},
		{PID: 613, Group: "other", Name: "job"},
	} {
		reg.Register(rec)
	}

	renames := map[string]string{"shop": "market", "shop@feat": "market@feat"}
	if n := reg.RenameGroups(renames); n != 2 {
		t.Fatalf("RenameGroups(%v) moved %d runs, want 2", renames, n)
	}
	for pid, want := range map[int]string{611: "market", 612: "market@feat", 613: "other"} {
		rec, ok := reg.Lookup(pid)
		if !ok {
			t.Errorf("pid %d is no longer registered after the rename", pid)
			continue
		}
		if rec.Group != want {
			t.Errorf("pid %d group = %q, want %q", pid, rec.Group, want)
		}
	}

	if n := reg.RenameGroups(nil); n != 0 {
		t.Errorf("a nil rename moved %d runs, want none", n)
	}
}
