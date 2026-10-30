package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

func TestListenerObservation(t *testing.T) {
	t.Run("the collector answered", func(t *testing.T) {
		env := fakeEnv(t)
		env.ObserveListeners = func(context.Context) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 3000, PID: 42}}, nil
		}
		got := run(t, env, checkListenerObservation)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Detail, "1 listener row") {
			t.Errorf("detail = %q, want the observed row count", got.Detail)
		}
	})

	t.Run("empty successful output is a fail with the collector's hint", func(t *testing.T) {
		env := fakeEnv(t)
		env.ObserveListeners = func(context.Context) ([]ports.ListeningPort, error) {
			return nil, &ports.ObservationError{
				Code:   ports.CodeObservationEmpty,
				Detail: "netstat: exited 0 but printed no listening table",
				Hint:   "retry outside the sandbox",
			}
		}
		got := run(t, env, checkListenerObservation)
		wantStatus(t, got, StatusFail)
		if got.Fix != "retry outside the sandbox" {
			t.Errorf("fix = %q, want the collector's hint", got.Fix)
		}
		if !strings.Contains(got.Detail, ports.CodeObservationEmpty) {
			t.Errorf("detail = %q, want the stable code", got.Detail)
		}
	})

	t.Run("an unclassified failure still names a next step", func(t *testing.T) {
		env := fakeEnv(t)
		env.ObserveListeners = func(context.Context) ([]ports.ListeningPort, error) {
			return nil, errors.New("no such command")
		}
		got := run(t, env, checkListenerObservation)
		wantStatus(t, got, StatusFail)
		if got.Fix == "" {
			t.Error("a failed observation must carry a fix")
		}
	})
}
