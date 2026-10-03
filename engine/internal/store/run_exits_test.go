package store

import (
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestRunExitsRoundTripAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	started := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	exited := started.Add(3 * time.Second)

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	row := RunExitRow{
		ID: "run-1", PID: 123, Group: "shop", Name: "api",
		Cmd: "python3 api.py", Cwd: "/home/me/code/shop", PortHint: 8123,
		StartedAt: started, ConfigPath: "/home/me/code/shop/oberth.yaml",
		StartID: "start-1", Origin: "cli", LogPath: "/tmp/api.log", LogOffset: 17,
		Code: 7, Reason: "crashed", ExitedAt: exited,
		LastLines: []string{"traceback", "boom"},
	}
	if err := first.AppendRunExit(row); err != nil {
		t.Fatalf("AppendRunExit: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()
	got, err := second.RunExits(0)
	if err != nil {
		t.Fatalf("RunExits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("RunExits = %d rows, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0], row) {
		t.Fatalf("RunExits[0] = %+v, want %+v", got[0], row)
	}
}

func TestOpenReadOnlyReadsRunExits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := first.AppendRunExit(RunExitRow{ID: "run-1", Group: "shop", Name: "api", Code: 7, Reason: "crashed", ExitedAt: time.Now()}); err != nil {
		t.Fatalf("AppendRunExit: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	readOnly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer readOnly.Close()
	rows, err := readOnly.RunExits(0)
	if err != nil {
		t.Fatalf("RunExits: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "run-1" || rows[0].Reason != "crashed" {
		t.Fatalf("rows = %+v, want the durable run exit", rows)
	}
}

func TestRunExitsKeepOnlyNewestRows(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	for i := 0; i < RunExitLimit+5; i++ {
		if err := s.AppendRunExit(RunExitRow{
			ID:        "run-" + strconv.Itoa(i),
			PID:       i + 1,
			Group:     "shop",
			Name:      "job",
			StartedAt: base.Add(time.Duration(i) * time.Second),
			ExitedAt:  base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("AppendRunExit(%d): %v", i, err)
		}
	}
	rows, err := s.RunExits(0)
	if err != nil {
		t.Fatalf("RunExits: %v", err)
	}
	if len(rows) != RunExitLimit {
		t.Fatalf("RunExits = %d rows, want %d", len(rows), RunExitLimit)
	}
	if rows[0].ID != "run-"+strconv.Itoa(RunExitLimit+4) {
		t.Errorf("newest row = %q, want run-%d", rows[0].ID, RunExitLimit+4)
	}
}
