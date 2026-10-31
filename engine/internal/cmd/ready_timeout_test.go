package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// A daemon that predates ready_timeout omits it; the client must keep the same
// 30s default the manifest would have applied.
func TestStartChunkReadyTimeoutFallsBackToDefault(t *testing.T) {
	if got := startChunkReadyTimeout(rpc.GroupsStartChunk{Service: "api"}); got != groups.DefaultReadyTimeout {
		t.Fatalf("old-daemon budget = %s, want %s", got, groups.DefaultReadyTimeout)
	}
	if got := startChunkReadyTimeout(rpc.GroupsStartChunk{Service: "api", ReadyTimeout: "45s"}); got != 45*time.Second {
		t.Fatalf("declared budget = %s, want 45s", got)
	}
	if got := startChunkReadyTimeout(rpc.GroupsStartChunk{Service: "api", ReadyTimeout: "nonsense"}); got != groups.DefaultReadyTimeout {
		t.Fatalf("unusable budget = %s, want the default", got)
	}
}

// An owned run is measured from its own StartedAt, so the time it already
// spent starting counts against its budget. A skipped service is measured from
// the wait start even though it has been running for a while.
func TestStartDeadlineAnchorsOwnedRunsAtStartedAt(t *testing.T) {
	waitStart := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	startedAt := waitStart.Add(-10 * time.Second).Format(time.RFC3339)
	svc := &state.Service{Name: "api", StartedAt: &startedAt}
	owned := startSelection{owned: true, budget: 30 * time.Second}
	if got, want := startDeadline(owned, svc, waitStart, time.Time{}), waitStart.Add(20*time.Second); !got.Equal(want) {
		t.Fatalf("owned deadline = %s, want %s", got, want)
	}
	skipped := startSelection{owned: false, budget: 30 * time.Second}
	if got, want := startDeadline(skipped, svc, waitStart, time.Time{}), waitStart.Add(30*time.Second); !got.Equal(want) {
		t.Fatalf("skipped deadline = %s, want %s", got, want)
	}
	if got, want := startDeadline(owned, nil, waitStart, time.Time{}), waitStart.Add(30*time.Second); !got.Equal(want) {
		t.Fatalf("unanchored deadline = %s, want %s", got, want)
	}
	hard := waitStart.Add(5 * time.Second)
	if got := startDeadline(owned, svc, waitStart, hard); !got.Equal(hard) {
		t.Fatalf("explicit cap deadline = %s, want %s", got, hard)
	}
	// A longer --wait-timeout is a ceiling, not a new budget: it must not
	// silently extend a service past its declared ready_timeout.
	longer := waitStart.Add(120 * time.Second)
	if got, want := startDeadline(owned, svc, waitStart, longer), waitStart.Add(20*time.Second); !got.Equal(want) {
		t.Fatalf("a longer --wait-timeout extended the budget: got %s, want %s", got, want)
	}
}

// The failure message must name what actually expired. A short explicit
// --wait-timeout is not the service's 30s ready budget, and printing it as one
// sends whoever reads the failure after the wrong number.
func TestStartTimeoutMessageDistinguishesCeilingFromBudget(t *testing.T) {
	waitStart := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	owned := startSelection{owned: true, budget: 30 * time.Second}

	budgetExpired := anchorStart(owned, nil, waitStart, waitStart.Add(120*time.Second))
	if got := startTimeoutMessage(budgetExpired, "api", 120*time.Second); !strings.Contains(got, "after 30s") ||
		!strings.Contains(got, "ready_timeout") || strings.Contains(got, "--wait-timeout") {
		t.Fatalf("budget message = %q, want the 30s ready budget", got)
	}

	capped := anchorStart(owned, nil, waitStart, waitStart.Add(5*time.Second))
	got := startTimeoutMessage(capped, "api", 5*time.Second)
	if !strings.Contains(got, "5s") || !strings.Contains(got, "--wait-timeout") || !strings.Contains(got, "30s") {
		t.Fatalf("ceiling message = %q, want the 5s ceiling and the 30s budget", got)
	}
}

// Without --wait-timeout each service gets its own budget, and the timeout
// message carries that service's budget rather than a single global number.
func TestWaitForStartReadyUsesEachServiceBudget(t *testing.T) {
	observableProbe(t)
	t.Setenv("BERTH_HOME", t.TempDir())
	old := noDaemonFlag
	noDaemonFlag = true
	t.Cleanup(func() { noDaemonFlag = old })
	dir := t.TempDir()
	path := filepath.Join(dir, groups.ConfigName)
	data := "name: demo\nservices:\n  - name: api\n    cmd: echo api\n    port: 19001\n  - name: worker\n    cmd: echo worker\n    port: 19002\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	chunks := []rpc.GroupsStartChunk{
		{Service: "api", State: "started", ReadyTimeout: "15ms"},
		{Service: "worker", State: "started", ReadyTimeout: "35ms"},
	}
	summary := rpc.GroupsStartEnd{Started: []string{"api", "worker"}}
	timedOut, err := waitForStartReady(context.Background(),
		rpc.GroupsStartParams{ConfigPath: &path}, nil, chunks, &summary, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(timedOut) != 2 || timedOut[0] != "api" || timedOut[1] != "worker" {
		t.Fatalf("timed out = %v, want both owned services", timedOut)
	}
	byName := map[string]string{}
	for _, chunk := range chunks {
		byName[chunk.Service] = chunk.Error
	}
	if !strings.Contains(byName["api"], "15ms") || !strings.Contains(byName["worker"], "35ms") {
		t.Fatalf("timeout messages = %+v, want each service's own budget", byName)
	}
}
