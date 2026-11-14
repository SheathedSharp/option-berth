package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// fakeKillCaller records the one call a timeout cleanup is allowed to make.
type fakeKillCaller struct {
	hello rpc.RuntimeServiceHelloResult
	calls []struct {
		method string
		params any
	}
}

func (f *fakeKillCaller) Call(_ context.Context, method string, params, result any) error {
	f.calls = append(f.calls, struct {
		method string
		params any
	}{method: method, params: params})
	if env, ok := result.(*rpc.KillEnvelope); ok {
		env.MutationResult.OK = true
	}
	return nil
}

func (f *fakeKillCaller) Hello() rpc.RuntimeServiceHelloResult { return f.hello }

// The cleanup stops the runs this invocation started, not whatever now answers
// to the service's name; that is the only thing that makes another controller's
// replacement run safe from this timeout.
func TestStopTimedOutServicesNamesTheRunsItStarted(t *testing.T) {
	caller := &fakeKillCaller{hello: rpc.RuntimeServiceHelloResult{
		Capabilities: []string{rpc.CapabilityKillExpectRuns},
	}}
	timedOut := []startTimeout{{service: "api", runID: "run-api"}, {service: "worker", runID: "run-worker"}}

	if err := stopTimedOutServices(context.Background(), caller, rpc.GroupsStartParams{}, timedOut); err != nil {
		t.Fatalf("stopTimedOutServices: %v", err)
	}
	if len(caller.calls) != 1 || caller.calls[0].method != "groups.kill" {
		t.Fatalf("calls = %+v, want one groups.kill", caller.calls)
	}
	params, ok := caller.calls[0].params.(rpc.GroupsKillParams)
	if !ok {
		t.Fatalf("params = %T, want rpc.GroupsKillParams", caller.calls[0].params)
	}
	if len(params.Only) != 0 {
		t.Fatalf("only = %v, must be empty: a name-scoped stop can reach a replacement run", params.Only)
	}
	if len(params.ExpectRuns) != 2 ||
		params.ExpectRuns[0] != (rpc.RunExpectation{Service: "api", RunID: "run-api"}) ||
		params.ExpectRuns[1] != (rpc.RunExpectation{Service: "worker", RunID: "run-worker"}) {
		t.Fatalf("expect_runs = %+v, want both started runs", params.ExpectRuns)
	}
	if !params.Release || params.Reason != "ready_timeout" {
		t.Fatalf("params = %+v, want the readiness-timeout stop", params)
	}
}

// A runtime service that predates expect_runs ignores it and stops by name. Sending the
// cleanup anyway is how it reaches the run somebody else started, so the stop
// is refused instead — and the error says the timed-out runs are still up.
func TestStopTimedOutServicesRefusesARuntimeServiceThatStopsByName(t *testing.T) {
	caller := &fakeKillCaller{hello: rpc.RuntimeServiceHelloResult{RuntimeServiceVersion: "0.9.0"}}

	err := stopTimedOutServices(context.Background(), caller,
		rpc.GroupsStartParams{}, []startTimeout{{service: "api", runID: "run-api"}})
	if err == nil {
		t.Fatal("a runtime service without expect_runs must not receive a name-only cleanup")
	}
	if !strings.Contains(err.Error(), "api") || !strings.Contains(err.Error(), "not stopped") ||
		!strings.Contains(err.Error(), "runtime service restart") {
		t.Fatalf("error = %v, want the services, what was not done, and the way out", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("calls = %+v, want no stop at all", caller.calls)
	}
}

// Same rule for a run this invocation cannot name: without the id there is no
// way to tell the timed-out run from its replacement, so nothing is signalled.
func TestStopTimedOutServicesRefusesAnUnnamedRun(t *testing.T) {
	caller := &fakeKillCaller{hello: rpc.RuntimeServiceHelloResult{
		Capabilities: []string{rpc.CapabilityKillExpectRuns},
	}}

	err := stopTimedOutServices(context.Background(), caller,
		rpc.GroupsStartParams{}, []startTimeout{{service: "api"}})
	if err == nil || !strings.Contains(err.Error(), "api") {
		t.Fatalf("error = %v, want a refusal naming the service", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("calls = %+v, want no stop at all", caller.calls)
	}
}
