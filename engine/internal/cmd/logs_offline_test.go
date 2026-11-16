package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// A stopped host must not turn a service log back into one undifferentiated
// file. The run offsets are on disk, so the same generation split the host
// would compute is available offline.
func TestOfflineRunRangeReadsOnlyTheCurrentGeneration(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	logPath := filepath.Join(t.TempDir(), "api.log")
	oldText := "old generation failed\n"
	newText := "new generation failed\n"
	if err := os.WriteFile(logPath, []byte(oldText+newText), 0o600); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	db, err := store.Open(store.Path())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, row := range []store.RunExitRow{
		{ID: "old", PID: 1, Group: "demo", Name: "api", LogPath: logPath,
			LogOffset: 0, Code: 1, Reason: "crashed",
			StartedAt: base, ExitedAt: base.Add(time.Minute)},
		{ID: "new", PID: 2, Group: "demo", Name: "api", LogPath: logPath,
			LogOffset: int64(len(oldText)), Code: 1, Reason: "crashed",
			StartedAt: base.Add(2 * time.Minute), ExitedAt: base.Add(3 * time.Minute)},
	} {
		if err := db.AppendRunExit(row); err != nil {
			t.Fatalf("AppendRunExit(%s): %v", row.ID, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	records, historyErr := offlineRunRecords(context.Background(), "demo")
	if historyErr != nil {
		t.Fatalf("offline history: %v", historyErr)
	}
	current, err := selectRunForLogs(records, "demo", "api", "current")
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if current.ID != "new" {
		t.Fatalf("current = %q, want the newest generation", current.ID)
	}
	doc, err := readFileLogsFrom(logPath, runLogRange(current, records), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(doc.Lines, "|"); got != strings.TrimSpace(newText) {
		t.Fatalf("current lines = %q, want only this generation", got)
	}

	older, err := selectRunForLogs(records, "demo", "api", "old")
	if err != nil {
		t.Fatal(err)
	}
	doc, err = readFileLogsFrom(logPath, runLogRange(older, records), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(doc.Lines, "|"); got != strings.TrimSpace(oldText) {
		t.Fatalf("old lines = %q, want only that generation", got)
	}
}
