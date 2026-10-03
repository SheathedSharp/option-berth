package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
)

// TestGroupsConfigWorktreePortsRoundTrip: set writes the key without
// disturbing comments, get reads it back, an edit that does not mention the
// key leaves it alone, and null removes it.
func TestGroupsConfigWorktreePortsRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := storeHarness(t, ctx)
	c := h.dial(ctx)

	parent := resolvedDir(t, t.TempDir())
	dir := filepath.Join(parent, "demo")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, configFixture)
	path := filepath.Join(dir, "oberth.yaml")

	get := func() json.RawMessage {
		t.Helper()
		var res struct {
			Config map[string]json.RawMessage `json:"config"`
		}
		if e := c.call("groups.config.get", rpc.GroupsConfigGetParams{Path: &path}, &res); e != nil {
			t.Fatalf("groups.config.get: %v", e)
		}
		v, ok := res.Config["worktree_ports"]
		if !ok {
			t.Fatalf("config has no worktree_ports key: %v", res.Config)
		}
		return v
	}
	set := func(params map[string]any) *rpc.GroupsConfigSetResult {
		t.Helper()
		params["path"] = path
		var res rpc.GroupsConfigSetResult
		if e := c.call("groups.config.set", params, &res); e != nil {
			t.Fatalf("groups.config.set %v: %v", params, e)
		}
		return &res
	}

	if got := string(get()); got != "null" {
		t.Fatalf("worktree_ports before any edit = %s, want null", got)
	}

	res := set(map[string]any{"worktree_ports": 4})
	if res.Config.WorktreePorts == nil || *res.Config.WorktreePorts != 4 {
		t.Fatalf("set result worktree_ports = %v, want 4", res.Config.WorktreePorts)
	}
	if len(res.Affected) != 0 {
		t.Errorf("affected = %v, want no service names", res.Affected)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# demo project", "# the database goes first", "worktree_ports: 4"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("file lost %q after the write:\n%s", want, data)
		}
	}
	if got := string(get()); got != "4" {
		t.Errorf("get after set = %s, want 4", got)
	}

	set(map[string]any{"services": []any{map[string]any{"name": "api", "patch": map[string]any{"icon": "server"}}}})
	if got := string(get()); got != "4" {
		t.Errorf("get after an unrelated edit = %s, want 4 kept", got)
	}

	res = set(map[string]any{"worktree_ports": nil})
	if res.Config.WorktreePorts != nil {
		t.Errorf("set result after null = %d, want nil", *res.Config.WorktreePorts)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), "worktree_ports") {
		t.Errorf("null did not remove the key:\n%s", data)
	}
	if got := string(get()); got != "null" {
		t.Errorf("get after null = %s, want null", got)
	}
}

// TestGroupsConfigWorktreePortsErrors: an out-of-range value is
// invalid_config and leaves the file byte-identical; an empty edit is invalid.
func TestGroupsConfigWorktreePortsErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _, path := configHarness(t, ctx)
	c := h.dial(ctx)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, n := range []int{0, 101} {
		e := c.call("groups.config.set", map[string]any{"path": path, "worktree_ports": n}, nil)
		if e == nil {
			t.Fatalf("worktree_ports %d should fail", n)
		}
		if e.Code != rpc.CodeInvalidConfig || !strings.Contains(e.Data.Detail, "worktree_ports") {
			t.Errorf("worktree_ports %d: error = %+v, want invalid_config naming the key", n, e)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("a failed write changed the file:\n%s", after)
	}

	if e := c.call("groups.config.set", map[string]any{"path": path}, nil); e == nil || e.Data.Code != "invalid_params" {
		t.Errorf("an empty edit: error = %+v, want invalid_params", e)
	}
}
