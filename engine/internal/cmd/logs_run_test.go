package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

func TestReadFileLogsFromStartsAtTheRunOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	earlier := "previous run: Shutting down\n"
	current := "booting\nERROR could not connect\nstill running\n"
	if err := os.WriteFile(path, []byte(earlier+current), 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := readFileLogsFrom(path, int64(len(earlier)), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(doc.Lines, "|"), "booting|ERROR could not connect|still running"; got != want {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if doc.Truncated || doc.RunStartUnknown {
		t.Fatalf("document = %+v, want a complete, known run", doc)
	}

	// The same read with a smaller tail reports only the end of this run.
	doc, err = readFileLogsFrom(path, int64(len(earlier)), 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(doc.Lines, "|"), "ERROR could not connect|still running"; got != want {
		t.Fatalf("tail = %q, want %q", got, want)
	}
	if !doc.Truncated {
		t.Fatal("a shortened run did not report truncation")
	}
}

func TestReadFileLogsFromReportsARotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(path, []byte("only current contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := readFileLogsFrom(path, 1<<20, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.RunStartUnknown {
		t.Fatal("an offset past the end did not mark the run start unknown")
	}
	if got := strings.Join(doc.Lines, "|"); got != "only current contents" {
		t.Fatalf("lines = %q, want the readable file", got)
	}
}

func TestRunTailStartIsTheLastLinesOfTheRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	earlier := "old run\n"
	current := "one\ntwo\nthree\n"
	if err := os.WriteFile(path, []byte(earlier+current), 0o600); err != nil {
		t.Fatal(err)
	}
	start, err := runTailStart(path, int64(len(earlier)), 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(len(earlier + "one\n")); start != want {
		t.Fatalf("tail start = %d, want %d", start, want)
	}
	if got := string(mustReadAt(t, path, start, 1<<10)); got != "two\nthree\n" {
		t.Fatalf("from tail start = %q, want this run's last two lines", got)
	}

	// A run shorter than the tail starts at its own offset, not at the file top.
	start, err = runTailStart(path, int64(len(earlier)), 10)
	if err != nil {
		t.Fatal(err)
	}
	if start != int64(len(earlier)) {
		t.Fatalf("short-run tail start = %d, want the run offset %d", start, len(earlier))
	}
}

func TestFindFirstErrorUsesTheFixedVocabulary(t *testing.T) {
	lines := []string{
		"booting",
		"listening on 3000",
		"ERROR could not connect to db",
		"Traceback (most recent call last)",
	}
	match := findFirstError(lines)
	if match == nil || match.Line != 3 || match.Text != "ERROR could not connect to db" {
		t.Fatalf("match = %+v, want line 3", match)
	}
	if got := findFirstError([]string{"all good", "errors: 0"}); got == nil || got.Line != 2 {
		t.Fatalf("match = %+v, want the fixed substring rule to include errors", got)
	}
	if got := findFirstError([]string{"all good"}); got != nil {
		t.Fatalf("match = %+v, want none", got)
	}
}

func TestLogsFirstErrorDocumentIsRunScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	earlier := "old ERROR from the previous run\n"
	current := "booting\nboom: Exception in worker\nstill running\n"
	if err := os.WriteFile(path, []byte(earlier+current), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := int64(len(earlier))
	meta := logMetadata{Service: "api", RunID: "run-2", LogOffset: &offset}

	prev := logsJSONFlag
	logsJSONFlag = true
	t.Cleanup(func() { logsJSONFlag = prev })

	out := captureStdout(t, func() {
		if err := logsFirstErrorFromFile(path, offset, meta); err != nil {
			t.Fatal(err)
		}
	})
	var doc logsDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if doc.FirstError == nil || doc.FirstError.Line != 2 {
		t.Fatalf("first_error = %+v, want line 2 of this run", doc.FirstError)
	}
	if doc.FirstError.Text != "boom: Exception in worker" {
		t.Fatalf("first_error text = %q", doc.FirstError.Text)
	}
	if doc.RunID != "run-2" || doc.LogOffset == nil || *doc.LogOffset != offset {
		t.Fatalf("metadata = run %q offset %v, want run-2 at %d", doc.RunID, doc.LogOffset, offset)
	}
	if doc.Lines == nil || len(doc.Lines) != 0 {
		t.Fatalf("lines = %v, want an empty array in first-error mode", doc.Lines)
	}
}

func TestLogsFirstErrorReportsNoMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(path, []byte("booting\nlistening\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := logsJSONFlag
	logsJSONFlag = true
	t.Cleanup(func() { logsJSONFlag = prev })

	out := captureStdout(t, func() {
		if err := logsFirstErrorFromFile(path, 0, logMetadata{Service: "api"}); err != nil {
			t.Fatal(err)
		}
	})
	var doc logsDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if doc.FirstError != nil || !doc.NoFirstError {
		t.Fatalf("document = %+v, want no_first_error", doc)
	}
}

func TestSelectRunForLogsPicksTheNewestRun(t *testing.T) {
	records := []rpc.RunRecord{
		{ID: "old", Group: "shop", Name: "api", StartedAt: "2026-10-31T09:00:00Z", Status: "exited"},
		{ID: "new", Group: "shop", Name: "api", StartedAt: "2026-10-31T10:00:00Z", Status: "running"},
		// A newer failed run must not become "current" while the older process
		// is still alive.
		{ID: "failed-newer", Group: "shop", Name: "api", StartedAt: "2026-10-31T10:30:00Z", Status: "exited"},
		{ID: "other", Group: "shop", Name: "worker", StartedAt: "2026-10-31T11:00:00Z", Status: "running"},
		{ID: "elsewhere", Group: "shop@other", Name: "api", StartedAt: "2026-10-31T12:00:00Z", Status: "running"},
	}
	rec, err := selectRunForLogs(records, "shop", "api", "current")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "new" {
		t.Fatalf("current = %q, want new", rec.ID)
	}
	rec, err = selectRunForLogs(records, "shop", "api", "old")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "old" {
		t.Fatalf("selected = %q, want old", rec.ID)
	}
	if _, err := selectRunForLogs(records, "shop", "api", "missing"); err == nil {
		t.Fatal("an unknown run ID was accepted")
	}
	if _, err := selectRunForLogs(records, "shop", "api", "elsewhere"); err == nil {
		t.Fatal("a run from another group was selected")
	}
}

func mustReadAt(t *testing.T, path string, offset, length int64) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, length)
	n, err := f.ReadAt(buf, offset)
	if err != nil && n == 0 {
		t.Fatal(err)
	}
	return buf[:n]
}
