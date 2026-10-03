package spawn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCancelledSpawnDoesNotInspectOrCreateResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	log := filepath.Join(root, "new", "run.log")
	// Cancellation wins before malformed input, cwd/executable lookup or log IO.
	for _, request := range []Request{
		{},
		{Argv: []string{"not-an-installed-command"}, Cwd: filepath.Join(root, "absent"), Detach: true, LogPath: log},
	} {
		h, err := Spawn(ctx, request)
		if h != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("handle=%v error=%v", h, err)
		}
	}
	if _, err := os.Stat(filepath.Dir(log)); !os.IsNotExist(err) {
		t.Fatalf("cancelled spawn created resources: %v", err)
	}
}
