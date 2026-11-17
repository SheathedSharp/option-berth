package cmd

import (
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// One recording becomes one record, and every reader sees the same fields.
// The live and ended conversions are the only paths from an entry or a store
// row to the wire, so a field present in one and missing in the other is how
// `logs --run` once lost the log offset and `restart --run` lost the argv.
func TestLiveEntryConversionCarriesEveryReaderFact(t *testing.T) {
	entry := runs.Entry{
		PID: 4242, ID: "run-1", Group: "shop", Name: "api",
		Cmd: "npm run dev -- --port 3000", Argv: []string{"npm", "run", "dev", "--", "--port", "3000"},
		Cwd: "/work/shop", StartedAt: "2026-11-16T09:00:00.123456789Z",
		PortHint: 3000, LogPath: "/tmp/api.log", LogOffset: 128,
	}
	rec := liveRunRecord(entry)

	if rec.Status != "running" || rec.LogPath != entry.LogPath {
		t.Fatalf("record = %+v, want a running record with its log path", rec)
	}
	if rec.LogOffset == nil || *rec.LogOffset != 128 {
		t.Fatalf("log_offset = %v, want 128", rec.LogOffset)
	}
	if !reflect.DeepEqual(rec.Argv, entry.Argv) {
		t.Fatalf("argv = %+v, want the recorded vector", rec.Argv)
	}
	if rec.StartedAt != "2026-11-16T09:00:00Z" {
		t.Fatalf("started_at = %q, want the RFC 3339 second form", rec.StartedAt)
	}
	if rec.Ports == nil {
		t.Fatal("ports = nil, want an empty array")
	}
	if rec.PortHint == nil || *rec.PortHint != 3000 || rec.URL == "" {
		t.Fatalf("port hint = %v url = %q, want both", rec.PortHint, rec.URL)
	}
}

func TestExitRowConversionCarriesEveryReaderFact(t *testing.T) {
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	row := store.RunExitRow{
		ID: "run-1", PID: 4242, Group: "shop", Name: "api",
		Cmd: "npm run dev", Argv: []string{"npm", "run", "dev"},
		Cwd: "/work/shop", StartedAt: at, ExitedAt: at.Add(time.Minute),
		LogPath: "/tmp/api.log", LogOffset: 128, Code: 1, Reason: "crashed",
		PortHint: 3000, LastLines: []string{"boom"},
	}
	rec := exitRunRecord(row)

	if rec.Status != "exited" || rec.Reason != "crashed" || rec.ExitCode == nil || *rec.ExitCode != 1 {
		t.Fatalf("record = %+v, want an exited record", rec)
	}
	if !reflect.DeepEqual(rec.Argv, row.Argv) {
		t.Fatalf("argv = %+v, want the recorded vector", rec.Argv)
	}
	if rec.LogOffset == nil || *rec.LogOffset != 128 {
		t.Fatalf("log_offset = %v, want 128", rec.LogOffset)
	}
	if rec.ExitedAt == "" || rec.Ports == nil || len(rec.LastLines) != 1 {
		t.Fatalf("record = %+v, want exit time, empty ports and last lines", rec)
	}
}

// The offline reader must present a run exactly as the host's runs.list does:
// the two paths answer the same question, so they may not disagree about a
// field. This is the regression guard for the duplicate conversion that dropped
// Argv on the offline path.
func TestOfflineRecordsMatchTheHostShape(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	t.Setenv("BERTH_DB", t.TempDir()+"/oberth.db")
	entry := runs.Entry{
		PID: 4242, ID: "run-1", Group: "shop", Name: "api",
		Argv: []string{"npm", "run", "dev"}, Cmd: "npm run dev",
		LogPath: "/tmp/api.log", LogOffset: 128, StartedAt: "2026-11-16T09:00:00Z",
	}
	want := liveRunRecord(entry)

	if err := runs.Add(entry); err != nil {
		t.Fatal(err)
	}
	records, _ := offlineRunRecords(t.Context(), "shop")
	if len(records) != 1 {
		t.Fatalf("records = %+v, want the one live run", records)
	}
	if !reflect.DeepEqual(records[0].Argv, want.Argv) {
		t.Fatalf("offline argv = %+v, want %+v", records[0].Argv, want.Argv)
	}
	if records[0].LogPath != want.LogPath || records[0].LogOffset == nil {
		t.Fatalf("offline record = %+v, want the host shape", records[0])
	}
}
