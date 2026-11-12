package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// worktreePortsEditor edits one project's manifest and reads the
// worktree_ports value back out of what the daemon serves.
type worktreePortsEditor struct {
	t    *testing.T
	cl   *testClient
	path string
}

// read returns worktree_ports as the daemon serves it, as raw JSON so "absent"
// and "null" stay distinguishable.
func (e *worktreePortsEditor) read() string {
	e.t.Helper()
	var served struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if callErr := e.cl.call("groups.config.get", rpc.GroupsConfigGetParams{Path: &e.path}, &served); callErr != nil {
		e.t.Fatalf("groups.config.get: %v", callErr)
	}
	value, ok := served.Config["worktree_ports"]
	if !ok {
		e.t.Fatalf("the served config has no worktree_ports key: %v", served.Config)
	}
	return string(value)
}

// write applies one edit to the project's manifest.
func (e *worktreePortsEditor) write(edit map[string]any) *rpc.GroupsConfigSetResult {
	e.t.Helper()
	edit["path"] = e.path
	var res rpc.GroupsConfigSetResult
	if callErr := e.cl.call("groups.config.set", edit, &res); callErr != nil {
		e.t.Fatalf("groups.config.set %v: %v", edit, callErr)
	}
	return &res
}

// TestGroupsConfigWorktreePortsRoundTrip: set writes the key without
// disturbing comments, get reads it back, an edit that does not mention the key
// leaves it alone, and null removes it.
func TestGroupsConfigWorktreePortsRoundTrip(t *testing.T) {
	h, _ := storeHarness(t)
	dir := filepath.Join(resolvedDir(t, t.TempDir()), "demo")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, demoManifest)
	editor := &worktreePortsEditor{
		t:    t,
		cl:   h.dial(t.Context()),
		path: filepath.Join(dir, groups.ConfigName),
	}

	if got := editor.read(); got != "null" {
		t.Fatalf("worktree_ports before any edit = %s, want null", got)
	}

	res := editor.write(map[string]any{"worktree_ports": 4})
	if res.Config.WorktreePorts == nil || *res.Config.WorktreePorts != 4 {
		t.Fatalf("set result worktree_ports = %v, want 4", res.Config.WorktreePorts)
	}
	if len(res.Affected) != 0 {
		t.Errorf("affected = %v, want no service names", res.Affected)
	}
	body, err := os.ReadFile(editor.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# the demo project",
		"# declared first: everything else depends on it",
		"worktree_ports: 4",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the file lost %q after the write:\n%s", want, body)
		}
	}
	if got := editor.read(); got != "4" {
		t.Errorf("get after set = %s, want 4", got)
	}

	// An edit that does not mention the key leaves it alone.
	editor.write(map[string]any{"services": []any{map[string]any{"name": "api", "patch": map[string]any{"icon": "server"}}}})
	if got := editor.read(); got != "4" {
		t.Errorf("get after an unrelated edit = %s, want the 4 kept", got)
	}

	res = editor.write(map[string]any{"worktree_ports": nil})
	if res.Config.WorktreePorts != nil {
		t.Errorf("set result after null = %d, want nil", *res.Config.WorktreePorts)
	}
	if body, _ := os.ReadFile(editor.path); strings.Contains(string(body), "worktree_ports") {
		t.Errorf("null did not remove the key:\n%s", body)
	}
	if got := editor.read(); got != "null" {
		t.Errorf("get after null = %s, want null", got)
	}
}

// TestGroupsConfigWorktreePortsErrors: an out-of-range value is invalid_config
// and leaves the file byte-identical; an empty edit is invalid.
func TestGroupsConfigWorktreePortsErrors(t *testing.T) {
	rig := newConfigRig(t)
	before := rig.manifest()

	for _, n := range []int{0, 101} {
		e := rig.cl.call("groups.config.set", map[string]any{"path": rig.file, "worktree_ports": n}, nil)
		if e == nil {
			t.Fatalf("worktree_ports %d was accepted", n)
		}
		if e.Code != rpc.CodeInvalidConfig || !strings.Contains(e.Data.Detail, "worktree_ports") {
			t.Errorf("worktree_ports %d: error = %+v, want invalid_config naming the key", n, e)
		}
	}
	if after := rig.manifest(); after != before {
		t.Errorf("a failed write changed the file:\n%s", after)
	}

	if e := rig.cl.call("groups.config.set", map[string]any{"path": rig.file}, nil); e == nil || e.Data.Code != "invalid_params" {
		t.Errorf("an empty edit: error = %+v, want invalid_params", e)
	}
}
