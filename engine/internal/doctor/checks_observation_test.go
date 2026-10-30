package doctor

import (
	"context"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

func TestListenerObservation(t *testing.T) {
	t.Run("sees its own listener", func(t *testing.T) {
		env := fakeEnv(t)
		env.ProbeObservation = func(context.Context) ports.ProbeResult {
			return ports.ProbeResult{State: ports.ObservationObservable, Detail: "saw 127.0.0.1:4321"}
		}
		got := run(t, env, checkListenerObservation)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Detail, "cli: observable") {
			t.Errorf("detail = %q, want the cli verdict", got.Detail)
		}
	})

	t.Run("blind keeps the probe hint", func(t *testing.T) {
		env := fakeEnv(t)
		env.ProbeObservation = func(context.Context) ports.ProbeResult {
			return ports.ProbeResult{
				State: ports.ObservationBlind, Code: ports.CodeObservationBlind,
				Detail: "bound 127.0.0.1:4321 but the table did not show it",
				Hint:   "retry outside the sandbox",
			}
		}
		got := run(t, env, checkListenerObservation)
		wantStatus(t, got, StatusFail)
		if got.Fix != "retry outside the sandbox" {
			t.Errorf("fix = %q, want the probe hint", got.Fix)
		}
		if !strings.Contains(got.Detail, "blind") {
			t.Errorf("detail = %q, want the blind verdict", got.Detail)
		}
	})

	t.Run("cannot bind at all", func(t *testing.T) {
		env := fakeEnv(t)
		env.ProbeObservation = func(context.Context) ports.ProbeResult {
			return ports.ProbeResult{
				State: ports.ObservationBindDenied, Code: ports.CodeObservationBindDenied,
				Detail: "cannot bind a local port",
				Hint:   "allow network access",
			}
		}
		got := run(t, env, checkListenerObservation)
		wantStatus(t, got, StatusFail)
		if got.Fix != "allow network access" {
			t.Errorf("fix = %q", got.Fix)
		}
	})

	t.Run("a blind daemon fails even when the cli can see", func(t *testing.T) {
		env := fakeEnv(t)
		env.ProbeObservation = func(context.Context) ports.ProbeResult {
			return ports.ProbeResult{State: ports.ObservationObservable}
		}
		env.Daemon = func(context.Context) DaemonInfo {
			return DaemonInfo{
				Reachable: true,
				Observation: &rpc.ObservationStatus{
					State: "blind", Code: ports.CodeObservationBlind,
					Detail: "bound but unseen", Hint: "restart the daemon outside the sandbox",
				},
			}
		}
		got := run(t, env, checkListenerObservation)
		wantStatus(t, got, StatusFail)
		if got.Fix != "restart the daemon outside the sandbox" {
			t.Errorf("fix = %q, want the daemon hint", got.Fix)
		}
		if !strings.Contains(got.Detail, "daemon: blind") {
			t.Errorf("detail = %q, want the daemon verdict", got.Detail)
		}
	})
}
