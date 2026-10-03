package store

import (
	"testing"
	"time"
)

// Fault injection is local to the disposable database; these definitions have
// not been run during the maintenance pass.
func TestRunExitAppendRollsBackWhenPruneFails(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < RunExitLimit; i++ {
		if err := s.AppendRunExit(RunExitRow{ID: "existing", PID: i + 1, StartedAt: base, ExitedAt: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_exit_prune BEFORE DELETE ON run_exits BEGIN SELECT RAISE(ABORT, 'prune fault'); END`); err != nil {
		t.Fatal(err)
	}
	err := s.AppendRunExit(RunExitRow{ID: "must-rollback", PID: 999, StartedAt: base, ExitedAt: base.Add(time.Hour)})
	if err == nil {
		t.Fatal("prune fault was ignored")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM run_exits`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != RunExitLimit {
		t.Fatalf("partial insert survived: %d", count)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM run_exits WHERE run_id='must-rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed event persisted: count=%d err=%v", count, err)
	}
}

func TestRunExitCorrectionDoesNotSelectReplacementPID(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := RunExitRow{ID: "old", PID: 42, StartedAt: base, ExitedAt: base.Add(time.Second), Reason: "crashed"}
	next := old
	next.ID = "replacement"
	next.StartedAt = base.Add(2 * time.Second)
	next.ExitedAt = base.Add(3 * time.Second)
	for _, row := range []RunExitRow{old, next} {
		if err := s.AppendRunExit(row); err != nil {
			t.Fatal(err)
		}
	}
	old.Reason = "stopped"
	if err := s.CorrectRunExit(old); err != nil {
		t.Fatal(err)
	}
	rows, err := s.RunExits(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "replacement" || rows[0].Reason != "crashed" || rows[1].Reason != "stopped" {
		t.Fatalf("wrong generation corrected: %+v", rows)
	}
}

func TestRunExitRenameUsesOriginalNamesForChainsAndSwaps(t *testing.T) {
	for _, rename := range []map[string]string{{"a": "b", "b": "c"}, {"a": "b", "b": "a"}} {
		s := openTemp(t)
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		for _, name := range []string{"a", "b"} {
			if err := s.AppendRunExit(RunExitRow{ID: name, Group: name, StartedAt: base, ExitedAt: base}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.RenameRunExitGroups(rename); err != nil {
			t.Fatal(err)
		}
		rows, err := s.RunExits(0)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Group != rename[row.ID] {
				t.Fatalf("cascading rename: %+v", rows)
			}
		}
	}
}
