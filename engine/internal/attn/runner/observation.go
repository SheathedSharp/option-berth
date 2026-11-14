package runner

import (
	"context"
	"errors"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"time"
)

var ErrConsentRevoked = errors.New("runner: assessment consent revoked")
var ErrConfigurationChanged = errors.New("runner: assessment configuration changed; reattach the task")

// WhileConfigured rechecks the current task's consent and provider identity on
// every poll. A changed endpoint, model, or credential ends the old session;
// it must not keep sending to a provider the task no longer describes.
func WhileConfigured(resolve func() model.Resolved, expected model.Client, read func(context.Context, string, string) (Status, error)) func(context.Context, string, string) (Status, error) {
	return func(ctx context.Context, binary, project string) (Status, error) {
		if err := CheckConfiguration(resolve, expected)(); err != nil {
			return Status{}, err
		}
		return read(ctx, binary, project)
	}
}

// AssessRecorded preserves failed attempts without counting validation as HTTP.
func AssessRecorded(ctx context.Context, binary, project, home string, client model.Client, task model.TaskContext,
	read func(context.Context, string, string) (Status, error), unavailableReason string) (Output, error) {
	started := time.Now()
	out, err := Auto(ctx, binary, project, client, task, read)
	if err == nil && unavailableReason != "" {
		out.Status, out.Reason = "unavailable", unavailableReason
	}
	out.TelemetryPath = AppendRunnerTelemetry(home, project, task, out, err, "runner", time.Since(started))
	return out, err
}

func attemptOutput(s Status, called, cached bool) Output {
	return Output{AttnPath: s.AttnPath, AttnRevision: s.AttnRevision, WorktreeRoot: s.Scope.Root, ModelCalled: called, Cached: cached}
}
func withAttempt(out Output, called, cached bool) Output {
	out.ModelCalled, out.Cached = called, cached
	return out
}

// ObservationLease is deliberately short even for a long polling interval.
// It attests only to a recent completed poll, never to PID/process liveness.
func ObservationLease(interval time.Duration) time.Duration {
	return min(time.Minute, max(15*time.Second, 3*interval))
}

// CheckConfiguration is also used while an event-driven task is idle. Revocation
// must not wait for a runtime event or cause an unnecessary status subprocess.
func CheckConfiguration(resolve func() model.Resolved, expected model.Client) func() error {
	return func() error {
		current := resolve()
		if !current.Enabled {
			return ErrConsentRevoked
		}
		next, err := current.Client()
		if err != nil || next.Endpoint != expected.Endpoint || next.Model != expected.Model || next.APIKey != expected.APIKey {
			return ErrConfigurationChanged
		}
		return nil
	}
}
