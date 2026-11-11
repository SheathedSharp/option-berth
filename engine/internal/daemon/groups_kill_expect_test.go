package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// killRuntime is a daemon runtime whose only observation is the rows the test
// hands it: no process is scanned, no signal can be sent, and every stop in
// these tests is a dry run.
func killRuntime(t *testing.T, rows []ports.ListeningPort) *Runtime {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	loop := scanner.New(scanner.Options{
		DaemonVersion: "test",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Scan:          func(scanner.Include) ([]ports.ListeningPort, error) { return rows, nil },
	})
	return &Runtime{
		Store:   st,
		Scanner: loop,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// timeoutCleanup sends the groups.kill a readiness timeout would send, as raw
// params so an unknown field is ignored rather than rejected: that is the only
// way a regression can show what the daemon does when it does not know the
// cleanup's run identity.
func timeoutCleanup(t *testing.T, rt *Runtime, params string) rpc.KillEnvelope {
	t.Helper()
	out, err := handleGroupsKill(context.Background(), &Request{
		Method:  "groups.kill",
		Params:  json.RawMessage(params),
		Runtime: rt,
	})
	if err != nil {
		t.Fatalf("groups.kill: %v", err)
	}
	env, ok := out.(rpc.KillEnvelope)
	if !ok {
		t.Fatalf("result = %T, want a kill envelope", out)
	}
	return env
}

// A readiness timeout's cleanup names the run it gave up on. Another controller
// that restarted the same service in the meantime owns a different run, and the
// stop must leave it alone.
func TestTimeoutCleanupStopsOnlyTheRunItStarted(t *testing.T) {
	manifest := "name: demo\nservices:\n  - name: api\n    cmd: sleep 1\n    port: 19001\n"

	t.Run("the replacement run is not selected", func(t *testing.T) {
		rt := killRuntime(t, []ports.ListeningPort{{
			Port: 19001, BindAddress: "127.0.0.1", PID: 424242,
			Group: "demo", RunID: "run-replacement", Tag: "api",
			RunGroup: "demo", RunRootPID: 424242,
		}})
		rt.Scanner.ObserveConfig(checkoutWithManifest(t, manifest))

		env := timeoutCleanup(t, rt,
			`{"name":"demo","expect_runs":[{"service":"api","run_id":"run-timed-out"}],"release":true,"reason":"ready_timeout","dry_run":true}`)
		if len(env.Results) != 0 {
			t.Fatalf("cleanup selected the replacement run's listener: %+v", env.Results)
		}
	})

	t.Run("the run this caller started is still selected", func(t *testing.T) {
		rt := killRuntime(t, []ports.ListeningPort{{
			Port: 19001, BindAddress: "127.0.0.1", PID: 424242,
			Group: "demo", RunID: "run-timed-out", Tag: "api",
			RunGroup: "demo", RunRootPID: 424242,
		}})
		rt.Scanner.ObserveConfig(checkoutWithManifest(t, manifest))

		env := timeoutCleanup(t, rt,
			`{"name":"demo","expect_runs":[{"service":"api","run_id":"run-timed-out"}],"release":true,"reason":"ready_timeout","dry_run":true}`)
		if len(env.Results) != 1 || env.Results[0].Port != 19001 {
			t.Fatalf("results = %+v, want the timed-out run's listener", env.Results)
		}
	})
}

// expect_runs is an ownership claim, so an incomplete or contradictory one is
// a caller error: silently falling back to a service-name stop is what lets a
// cleanup reach a replacement run.
func TestTimeoutCleanupRejectsAnIncompleteClaim(t *testing.T) {
	manifest := "name: demo\nservices:\n  - name: api\n    cmd: sleep 1\n    port: 19001\n"
	cases := []struct {
		name   string
		params string
		code   int
		want   string
	}{
		{
			name:   "both selectors",
			params: `{"name":"demo","only":["api"],"expect_runs":[{"service":"api","run_id":"run-1"}]}`,
			code:   rpc.CodeInvalidParams, want: "mutually exclusive",
		},
		{
			name:   "no run id",
			params: `{"name":"demo","expect_runs":[{"service":"api"}]}`,
			code:   rpc.CodeInvalidParams, want: "without a run id",
		},
		{
			name:   "undeclared service",
			params: `{"name":"demo","expect_runs":[{"service":"nope","run_id":"run-1"}]}`,
			code:   rpc.CodeNotFound, want: "not declared",
		},
		{
			name:   "same service twice",
			params: `{"name":"demo","expect_runs":[{"service":"api","run_id":"run-1"},{"service":"api","run_id":"run-2"}]}`,
			code:   rpc.CodeInvalidParams, want: "more than once",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := killRuntime(t, nil)
			rt.Scanner.ObserveConfig(checkoutWithManifest(t, manifest))
			_, err := handleGroupsKill(context.Background(), &Request{
				Method: "groups.kill", Params: json.RawMessage(tc.params), Runtime: rt,
			})
			if err == nil {
				t.Fatal("want a refusal")
			}
			if got := rpcCode(err); got != tc.code {
				t.Fatalf("code = %d, want %d (%v)", got, tc.code, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
