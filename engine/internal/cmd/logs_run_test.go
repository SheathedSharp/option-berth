package cmd

import (
	"context"
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

	doc, err := readFileLogsFrom(path, logRange{start: int64(len(earlier)), end: -1}, 10)
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
	doc, err = readFileLogsFrom(path, logRange{start: int64(len(earlier)), end: -1}, 2)
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
	doc, err := readFileLogsFrom(path, logRange{start: 1 << 20, end: -1}, 10)
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
	start, err := runTailStart(path, logRange{start: int64(len(earlier)), end: -1}, 2)
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
	start, err = runTailStart(path, logRange{start: int64(len(earlier)), end: -1}, 10)
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
		if err := logsFirstErrorFromFile(path, logRange{start: offset, end: -1}, meta); err != nil {
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
		if err := logsFirstErrorFromFile(path, logRange{start: 0, end: -1}, logMetadata{Service: "api"}); err != nil {
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

// A run's read must stop where the next generation of the same service began
// writing: the file is shared, and bytes past that point are another run's
// output — including the error that a later run crashed on.
func TestReadFileLogsFromStopsWhereTheNextRunBegan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	first := "booting\nlistening on 3000\n"
	second := "ERROR could not connect\n"
	if err := os.WriteFile(path, []byte(first+second), 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := readFileLogsFrom(path, logRange{start: 0, end: int64(len(first))}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(doc.Lines, "|"), "booting|listening on 3000"; got != want {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if doc.RunStartUnknown {
		t.Fatal("a range that is present in the file was reported as unknown")
	}
}

func TestLogsFirstErrorDoesNotBorrowALaterRunsLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	first := "booting\nlistening on 3000\n"
	second := "ERROR could not connect\n"
	if err := os.WriteFile(path, []byte(first+second), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := int64(0)
	prev := logsJSONFlag
	logsJSONFlag = true
	t.Cleanup(func() { logsJSONFlag = prev })

	out := captureStdout(t, func() {
		if err := logsFirstErrorFromFile(path, logRange{start: offset, end: int64(len(first))},
			logMetadata{Service: "api", RunID: "run-1", LogOffset: &offset}); err != nil {
			t.Fatal(err)
		}
	})
	var doc logsDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if doc.FirstError != nil {
		t.Fatalf("first_error = %+v, want none: that line belongs to the next run", doc.FirstError)
	}
	if !doc.NoFirstError {
		t.Fatalf("document = %+v, want no_first_error", doc)
	}
}

// runLogRange is the rule the read above depends on: a finished run ends where
// the next generation started, and a run that is still writing has no end.
func TestRunLogRangeEndsWhereTheNextGenerationStarted(t *testing.T) {
	first := rpc.RunRecord{ID: "r1", Group: "shop", Name: "api", Status: "exited",
		LogPath: "/logs/api.log", StartedAt: "2026-11-01T10:00:00Z", ExitedAt: "2026-11-01T10:02:00Z",
		LogOffset: int64Ptr(0)}
	second := rpc.RunRecord{ID: "r2", Group: "shop", Name: "api", Status: "exited",
		LogPath: "/logs/api.log", StartedAt: "2026-11-01T10:05:00Z", ExitedAt: "2026-11-01T10:07:00Z",
		LogOffset: int64Ptr(40)}
	live := rpc.RunRecord{ID: "r3", Group: "shop", Name: "api", Status: "running",
		LogPath: "/logs/api.log", StartedAt: "2026-11-01T10:08:00Z", LogOffset: int64Ptr(90)}
	records := []rpc.RunRecord{live, second, first}

	if got := runLogRange(first, records); got != (logRange{start: 0, end: 40}) {
		t.Fatalf("first run range = %+v, want [0,40)", got)
	}
	if got := runLogRange(second, records); got != (logRange{start: 40, end: 90}) {
		t.Fatalf("second run range = %+v, want [40,90)", got)
	}
	// The run that is still writing owns everything after its own start.
	if got := runLogRange(live, records); got != (logRange{start: 90, end: -1}) {
		t.Fatalf("live run range = %+v, want [90,EOF)", got)
	}
}

func TestRunLogRangeIgnoresRunsThatDidNotWriteAfterThisOne(t *testing.T) {
	first := rpc.RunRecord{ID: "r1", Group: "shop", Name: "api", Status: "exited",
		LogPath: "/logs/api.log", StartedAt: "2026-11-01T10:00:00Z", ExitedAt: "2026-11-01T10:02:00Z",
		LogOffset: int64Ptr(0)}
	// Started while the first run was still alive: its bytes are interleaved
	// with this run's, so no byte position separates the two.
	overlapping := rpc.RunRecord{ID: "r2", Group: "shop", Name: "api", Status: "exited",
		LogPath: "/logs/api.log", StartedAt: "2026-11-01T10:01:00Z", ExitedAt: "2026-11-01T10:03:00Z",
		LogOffset: int64Ptr(10)}
	// Another file entirely, and a record whose offset was never written down.
	elsewhere := rpc.RunRecord{ID: "r3", Group: "shop@old", Name: "api", Status: "exited",
		LogPath: "/other/api.log", StartedAt: "2026-11-01T10:05:00Z", ExitedAt: "2026-11-01T10:06:00Z",
		LogOffset: int64Ptr(5)}
	unrecorded := rpc.RunRecord{ID: "r4", Group: "shop", Name: "api", Status: "exited",
		LogPath: "/logs/api.log", StartedAt: "2026-11-01T10:07:00Z", ExitedAt: "2026-11-01T10:08:00Z"}

	records := []rpc.RunRecord{first, overlapping, elsewhere, unrecorded}
	if got := runLogRange(first, records); got != (logRange{start: 0, end: -1}) {
		t.Fatalf("range = %+v, want [0,EOF): nothing recorded where this run's bytes end", got)
	}
}

// A rotation between two runs leaves a later run's offset behind this one's:
// the file no longer holds this run at all, and the read says so instead of
// showing the newer generation.
func TestReadFileLogsFromReportsARotationBehindTheRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(path, []byte("a fresh generation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := readFileLogsFrom(path, logRange{start: 40, end: 10}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.RunStartUnknown {
		t.Fatal("a range that ends behind its start was not reported as unknown")
	}
}

// Following a run that has ended prints its lines and returns: nothing that
// arrives later can belong to that run, so waiting would only show the next
// generation's output under this run's name.
func TestFollowOfAFinishedRunPrintsItsLinesAndStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	first := "booting\nlistening on 3000\n"
	second := "ERROR could not connect\n"
	if err := os.WriteFile(path, []byte(first+second), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := streamFileLogsFrom(context.Background(), path,
			logRange{start: 0, end: int64(len(first))}, false, 10, logMetadata{Service: "api"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "booting") || !strings.Contains(out, "listening on 3000") {
		t.Fatalf("output = %q, want this run's lines", out)
	}
	if strings.Contains(out, "ERROR") {
		t.Fatalf("output = %q, want nothing from the next run", out)
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
