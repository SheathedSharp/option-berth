package runtimesvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// storeHarness is a harness whose temp database is already open and which sees
// one port listening — what every write path needs.
func storeHarness(t *testing.T, rows ...ports.ListeningPort) (*testHarness, *store.Store) {
	t.Helper()
	// The temp directory is claimed first so its removal is the last cleanup to
	// run: the harness keeps the database inside it and must close it before
	// anything deletes the file.
	db := filepath.Join(t.TempDir(), "option-berth.db")
	h := newHarness(t, t.Context())
	st := h.withStore(db)
	if len(rows) == 0 {
		// One plain user process is enough: the write paths only need a row to
		// attribute history to.
		rows = []ports.ListeningPort{
			{Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server"},
		}
	}
	h.setRows(rows...)
	return h, st
}

func TestConfigRoundTripThroughTheDaemon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	h := newHarness(t, t.Context())
	cl := h.dial(t.Context())

	var configPath rpc.ConfigPathResult
	if e := cl.call("config.path", rpc.Empty{}, &configPath); e != nil {
		t.Fatalf("config.path: %v", e)
	}
	// Two things are asserted here: the daemon resolves the path this side
	// computes (one cross-process layout), and it lands inside the isolated
	// home. The `.config/option-berth` shape is deliberately not hard-coded —
	// the layout moved once, and treating the shape as an invariant would cry
	// wolf every time it moves again.
	if want := filepath.Join(paths.Dir(), paths.ConfigFile); configPath.Path != want {
		t.Errorf("path = %q, want %q", configPath.Path, want)
	}
	if !strings.HasPrefix(configPath.Path, home+string(filepath.Separator)) {
		t.Errorf("path = %q, want it under the temp home %s", configPath.Path, home)
	}

	var setResult rpc.ConfigSetResult
	if e := cl.call("config.set", rpc.ConfigSetParams{
		Patch: map[string]any{"daemon": map[string]any{"log_level": "debug"}},
	}, &setResult); e != nil {
		t.Fatalf("config.set: %v", e)
	}
	if !setResult.OK {
		t.Error("config.set did not report ok")
	}
	if _, err := os.Stat(configPath.Path); err != nil {
		t.Fatalf("config.set wrote no file: %v", err)
	}

	var gotConfig rpc.ConfigGetResult
	if e := cl.call("config.get", rpc.Empty{}, &gotConfig); e != nil {
		t.Fatalf("config.get: %v", e)
	}
	if level := gotConfig.Config["daemon"].(map[string]any)["log_level"]; level != "debug" {
		t.Errorf("config = %v, want daemon.log_level debug", gotConfig.Config)
	}

	// A second write keeps the previous file next to it.
	if e := cl.call("config.set", rpc.ConfigSetParams{
		Patch: map[string]any{"daemon": map[string]any{"log_level": "info"}},
	}, nil); e != nil {
		t.Fatalf("the second config.set: %v", e)
	}
	if _, err := os.Stat(configPath.Path + ".bak"); err != nil {
		t.Errorf("no backup beside the config: %v", err)
	}

	if e := cl.call("config.set", rpc.ConfigSetParams{
		Patch: map[string]any{"daemon": map[string]any{"log_level": "sideways"}},
	}, nil); e == nil || e.Data.Code != "invalid_params" {
		t.Errorf("error = %+v, want invalid_params for a rejected value", e)
	}
}
