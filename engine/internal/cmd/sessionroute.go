package cmd

import (
	"context"
	"errors"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
)

// sessionsRuntimeService connects without autostarting. Session membership lives in
// the runtime service's run registry, so commands that act on a session must say when
// that registry is unavailable.
func sessionsRuntimeService(ctx context.Context) (*client.Client, error) {
	c, err := dialRuntimeService(ctx)
	if err != nil {
		if errors.Is(err, client.ErrNotRunning) {
			return nil, errors.New("sessions need a running runtime service; start one with `oberth runtime service run --detach`")
		}
		return nil, err
	}
	return c, nil
}

// currentSession resolves the `current` shorthand used by --session from the
// agent environment of this shell.
func currentSession(id string) (string, error) {
	if !strings.EqualFold(strings.TrimSpace(id), "current") {
		return id, nil
	}
	s, ok := sessions.Detect(sessions.Options{})
	if !ok {
		return "", errors.New("no agent session in this environment; pass a session id, or set BERTH_SESSION")
	}
	return s.ID, nil
}
