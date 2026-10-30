package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
)

// A blind process must not create a daemon that outlives it, and the hint has
// to name the command that does help (docs/cli.md): starting the daemon from a
// terminal that can observe listeners. The probe's own hint talks about the
// sandbox and doctor, so the refusal must add the command rather than replace
// its own hint with whatever the probe happened to say.
func TestAutostartRefusalNamesServeDetach(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	t.Setenv("BERTH_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	t.Setenv(client.NoAutostartEnv, "")
	blindObservation(t)

	_, err := connectForWrite(context.Background())
	if err == nil {
		t.Fatal("connectForWrite autostarted a daemon from a blind process")
	}
	var ce *cliErr
	if !errors.As(err, &ce) {
		t.Fatalf("err = %T (%v), want *cliErr", err, err)
	}
	if ce.Code != "observation_blind" {
		t.Errorf("code = %q, want observation_blind", ce.Code)
	}
	if !strings.Contains(ce.Hint, "oberth serve --detach") {
		t.Errorf("hint = %q, want the command that starts a daemon from a normal terminal", ce.Hint)
	}
}
