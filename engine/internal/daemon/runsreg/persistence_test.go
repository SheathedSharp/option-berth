package runsreg

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/sheathedsharp/option-berth/internal/store"
)

func TestMirrorFailuresAreReportedWithoutErasingRuntimeFacts(t *testing.T) {
	r, rec := mirroredRegistry(t)
	var logs bytes.Buffer
	r.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	if err := os.WriteFile(runs.Path(), []byte("invalid ledger"), 0600); err != nil {
		t.Fatal(err)
	}
	rec.Cmd = "private-command-must-not-be-logged"
	r.Register(rec)
	if _, ok := r.Lookup(rec.PID); !ok {
		t.Fatal("disk failure erased live process fact")
	}
	r.Unregister(rec.PID)
	if _, ok := r.Lookup(rec.PID); ok {
		t.Fatal("disk failure retained unregistered process")
	}
	text := logs.String()
	if !strings.Contains(text, "mirror.add") || !strings.Contains(text, "mirror.remove") || strings.Contains(text, rec.Cmd) {
		t.Fatalf("incorrect diagnostics: %s", text)
	}
	data, err := os.ReadFile(runs.Path())
	if err != nil || string(data) != "invalid ledger" {
		t.Fatalf("failed write overwrote evidence: %q %v", data, err)
	}
}

func TestExitWriteFailureIsVisibleAndKeepsObservedExit(t *testing.T) {
	r := testRegistry(42)
	var logs bytes.Buffer
	r.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	r.SetHistoryStore(&store.Store{}) // deliberately unopened; no real database
	e := r.StartFailed(Record{PID: 42, Name: "worker"}, "private-log-tail")
	if e.ID == "" || len(r.Exits()) != 1 {
		t.Fatal("failed durable write erased observed failure")
	}
	if !strings.Contains(logs.String(), "exit.append") || strings.Contains(logs.String(), "private-log-tail") {
		t.Fatalf("incorrect diagnostics: %s", logs.String())
	}
}

func TestFutureExitIsNotCorrectedAsRecent(t *testing.T) {
	r := testRegistry()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.StartFailed(Record{PID: 42}, "failed")
	r.mu.Lock()
	r.exits[0].ExitedAt = now.Add(time.Hour)
	r.mu.Unlock()
	r.Stopping([]int{42})
	if got := r.Exits()[0].Reason; got != ReasonStartFailed {
		t.Fatalf("future evidence accepted: %s", got)
	}
}
