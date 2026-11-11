package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// startRowsFrom replaces the wait's observation with one group row set, so a
// readiness rule is decided on rows a test controls instead of a daemon or the
// host's listener table.
func startRowsFrom(t *testing.T, groups ...state.Group) {
	t.Helper()
	prev := startRows
	startRows = func(context.Context) ([]ports.ListeningPort, []state.Group, *rpc.ObservationStatus, error) {
		return nil, groups, &rpc.ObservationStatus{State: string(ports.ObservationObservable)}, nil
	}
	t.Cleanup(func() { startRows = prev })
}

// startManifest writes one project manifest and returns its path.
func startManifest(t *testing.T, services string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(path, []byte("name: demo\nservices:\n"+services), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestWaitForStartReadyCreditsOnlyTheRunItIsAbout: the wait is about a run, not
// about whatever answers to the service's name. R1 was started here and is
// still waiting; another controller restarts the same service as R2, and R2
// becomes healthy. R2's health is evidence about R2 — reading it as R1's
// readiness reports a service that was never confirmed, and the run that is
// actually healthy is not this invocation's to stop.
func TestWaitForStartReadyCreditsOnlyTheRunItIsAbout(t *testing.T) {
	port, healthz := 19001, "/healthz"
	for _, tc := range []struct {
		name    string
		chunk   rpc.GroupsStartChunk
		service state.Service
	}{
		{
			name:  "a healthy replacement listener",
			chunk: rpc.GroupsStartChunk{Service: "api", State: "started", RunID: "run-r1"},
			service: state.Service{PortActual: &port, Health: &healthz,
				HealthStatus: &state.Health{Status: state.HealthOK}},
		},
		{
			name:    "a healthy replacement worker",
			chunk:   rpc.GroupsStartChunk{Service: "api", State: "started", RunID: "run-r1"},
			service: state.Service{},
		},
		{
			name:  "a skipped run the chunk named",
			chunk: rpc.GroupsStartChunk{Service: "api", State: "skipped", Skipped: true, RunID: "run-r1"},
			service: state.Service{PortActual: &port, Health: &healthz,
				HealthStatus: &state.Health{Status: state.HealthOK}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := startManifest(t, "  - name: api\n    cmd: echo api\n")
			replacement := "run-r2"
			svc := tc.service
			svc.Name = "api"
			svc.Running = true
			svc.RunID = &replacement
			startRowsFrom(t, state.Group{Name: "demo", ConfigPath: &path, Services: []state.Service{svc}})

			chunk := tc.chunk
			chunk.ReadyTimeout = "250ms"
			chunks := []rpc.GroupsStartChunk{chunk}
			summary := rpc.GroupsStartEnd{Started: []string{"api"}, Errors: []string{}}
			timedOut, err := waitForStartReady(context.Background(), rpc.GroupsStartParams{ConfigPath: &path},
				nil, chunks, &summary, 0, false)
			if err != nil {
				t.Fatal(err)
			}
			if chunks[0].Ready != nil && *chunks[0].Ready {
				t.Fatalf("run %s was reported ready on run %s's evidence", "run-r1", replacement)
			}
			if chunks[0].State != "failed" || chunks[0].Reason != "run_replaced" {
				t.Fatalf("chunk = %+v, want a run_replaced failure naming the replacement", chunks[0])
			}
			if len(summary.Errors) != 1 || summary.Errors[0] != "api" {
				t.Fatalf("summary errors = %v, want api", summary.Errors)
			}
			if len(timedOut) != 0 {
				t.Fatalf("cleanup = %v, want none: the replacement is not this invocation's to stop", timedOut)
			}
		})
	}
}

// TestWaitForStartReadyConfirmsItsOwnRun is the other half of the identity
// rule: the same row shape, naming the run this invocation started, is ready.
// A rule that fails every named run would satisfy the test above and break
// every ordinary wait.
func TestWaitForStartReadyConfirmsItsOwnRun(t *testing.T) {
	port, healthz := 19001, "/healthz"
	path := startManifest(t, "  - name: api\n    cmd: echo api\n")
	started := "run-r1"
	startRowsFrom(t, state.Group{Name: "demo", ConfigPath: &path, Services: []state.Service{{
		Name: "api", Running: true, RunID: &started, PortActual: &port, Health: &healthz,
		HealthStatus: &state.Health{Status: state.HealthOK},
	}}})

	chunks := []rpc.GroupsStartChunk{{Service: "api", State: "started", RunID: started, ReadyTimeout: "250ms"}}
	summary := rpc.GroupsStartEnd{Started: []string{"api"}, Errors: []string{}}
	if _, err := waitForStartReady(context.Background(), rpc.GroupsStartParams{ConfigPath: &path},
		nil, chunks, &summary, 0, false); err != nil {
		t.Fatal(err)
	}
	if chunks[0].Ready == nil || !*chunks[0].Ready {
		t.Fatalf("chunk = %+v, want the run this invocation started reported ready", chunks[0])
	}
	if len(summary.Errors) != 0 {
		t.Fatalf("summary errors = %v, want none", summary.Errors)
	}
}

// TestWaitForStartReadyWillNotReadAnUnattributedListener: the row says
// something is listening and healthy, but it names no run. A listener the row
// cannot attribute is some process on the service's address, not the run this
// wait started, so it does not end the wait as ready.
func TestWaitForStartReadyWillNotReadAnUnattributedListener(t *testing.T) {
	port, healthz := 19001, "/healthz"
	path := startManifest(t, "  - name: api\n    cmd: echo api\n")
	startRowsFrom(t, state.Group{Name: "demo", ConfigPath: &path, Services: []state.Service{{
		Name: "api", Running: true, PortActual: &port, Health: &healthz,
		HealthStatus: &state.Health{Status: state.HealthOK},
	}}})

	chunks := []rpc.GroupsStartChunk{{Service: "api", State: "started", RunID: "run-r1", ReadyTimeout: "100ms"}}
	summary := rpc.GroupsStartEnd{Started: []string{"api"}, Errors: []string{}}
	timedOut, err := waitForStartReady(context.Background(), rpc.GroupsStartParams{ConfigPath: &path},
		nil, chunks, &summary, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if chunks[0].Ready != nil && *chunks[0].Ready {
		t.Fatal("a listener the row cannot attribute was credited to the run this wait started")
	}
	if chunks[0].State != "failed" || chunks[0].Reason != "ready_timeout" {
		t.Fatalf("chunk = %+v, want the budget to expire unconfirmed", chunks[0])
	}
	if len(timedOut) != 1 || timedOut[0].service != "api" || timedOut[0].runID != "run-r1" {
		t.Fatalf("cleanup = %v, want the run this invocation started", timedOut)
	}
}

// TestWaitForStartReadyKeepsLegacyChunksUsable: a daemon that predates run ids
// names no run to compare against. There is no identity evidence, so the wait
// keeps reading the service row rather than refusing every old daemon.
func TestWaitForStartReadyKeepsLegacyChunksUsable(t *testing.T) {
	port, healthz := 19001, "/healthz"
	path := startManifest(t, "  - name: api\n    cmd: echo api\n")
	live := "run-whatever"
	startRowsFrom(t, state.Group{Name: "demo", ConfigPath: &path, Services: []state.Service{{
		Name: "api", Running: true, RunID: &live, PortActual: &port, Health: &healthz,
		HealthStatus: &state.Health{Status: state.HealthOK},
	}}})

	chunks := []rpc.GroupsStartChunk{{Service: "api", State: "started", ReadyTimeout: "250ms"}}
	summary := rpc.GroupsStartEnd{Started: []string{"api"}, Errors: []string{}}
	if _, err := waitForStartReady(context.Background(), rpc.GroupsStartParams{ConfigPath: &path},
		nil, chunks, &summary, 0, false); err != nil {
		t.Fatal(err)
	}
	if chunks[0].Ready == nil || !*chunks[0].Ready {
		t.Fatalf("chunk = %+v, want an unnamed run read from the service row", chunks[0])
	}
}
