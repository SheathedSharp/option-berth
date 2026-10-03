package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/paths"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// storeHarness is a harness with a temp database already open and one port
// listening, which is what every write path needs.
func storeHarness(t *testing.T, ctx context.Context, rows ...ports.ListeningPort) (*testHarness, *store.Store) {
	t.Helper()
	// The temp directory is claimed first so its removal is the last cleanup
	// to run: the harness holds the database inside it and has to close it
	// before anything deletes the file.
	db := filepath.Join(t.TempDir(), "option-berth.db")
	h := newHarness(t, ctx)
	st := h.withStore(db)
	if len(rows) == 0 {
		rows = []ports.ListeningPort{{
			Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server",
		}}
	}
	h.setRows(rows...)
	return h, st
}

func TestConfigRoundTripThroughTheDaemon(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	h := newHarness(t, ctx)
	c := h.dial(ctx)

	var path rpc.ConfigPathResult
	if e := c.call("config.path", rpc.Empty{}, &path); e != nil {
		t.Fatalf("config.path: %v", e)
	}
	// 断言两件事：daemon 解析出来的路径**和这边自己算的一致**（跨进程布局一致），
	// 以及它落在隔离的 home 里。刻意不写死 `.config/option-berth` 那种形状 ——
	// 布局改过一次，把形状当不变量的断言会在每次搬家时假报警。
	if want := filepath.Join(paths.Dir(), paths.ConfigFile); path.Path != want {
		t.Errorf("path = %q, want %q", path.Path, want)
	}
	if !strings.HasPrefix(path.Path, home+string(filepath.Separator)) {
		t.Errorf("path = %q, want it under the temp home %s", path.Path, home)
	}

	var set rpc.ConfigSetResult
	if e := c.call("config.set", rpc.ConfigSetParams{
		Patch: map[string]any{"daemon": map[string]any{"log_level": "debug"}},
	}, &set); e != nil {
		t.Fatalf("config.set: %v", e)
	}
	if !set.OK {
		t.Error("config.set did not report ok")
	}
	if _, err := os.Stat(path.Path); err != nil {
		t.Fatalf("config.set wrote no file: %v", err)
	}

	var got rpc.ConfigGetResult
	if e := c.call("config.get", rpc.Empty{}, &got); e != nil {
		t.Fatalf("config.get: %v", e)
	}
	if got.Config["daemon"].(map[string]any)["log_level"] != "debug" {
		t.Errorf("config = %v, want daemon.log_level debug", got.Config)
	}

	// A second write keeps the previous file next to it.
	if e := c.call("config.set", rpc.ConfigSetParams{
		Patch: map[string]any{"daemon": map[string]any{"log_level": "info"}},
	}, nil); e != nil {
		t.Fatalf("config.set: %v", e)
	}
	if _, err := os.Stat(path.Path + ".bak"); err != nil {
		t.Errorf("no backup beside the config: %v", err)
	}

	if e := c.call("config.set", rpc.ConfigSetParams{
		Patch: map[string]any{"daemon": map[string]any{"log_level": "sideways"}},
	}, nil); e == nil || e.Data.Code != "invalid_params" {
		t.Errorf("error = %+v, want invalid_params for a rejected value", e)
	}
}
