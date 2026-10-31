package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// A daemon that predates ready_timeout would ignore the declared budget and
// keep its fixed 30s dependency wait. Starting must say so instead of letting
// the file's promise silently not apply; stopping what already runs does not
// need the budget and stays allowed.
func TestConfigSupportRequiresReadyTimeoutCapability(t *testing.T) {
	declared := &groups.Config{
		Path:     "/tmp/oberth.yaml",
		Services: []groups.Service{{Name: "api", Cmd: "run api", ReadyTimeout: "90s"}},
	}
	plain := &groups.Config{
		Path:     "/tmp/oberth.yaml",
		Services: []groups.Service{{Name: "api", Cmd: "run api"}},
	}
	// Cmd without a ${...} reference, so only the ready_timeout gate is at
	// stake: UsesAssignedPorts stays false for this manifest.
	old := rpc.DaemonHelloResult{
		DaemonVersion: "v1.3.0",
		Capabilities:  []string{rpc.CapabilityAutoPorts, rpc.CapabilityManifestExtras},
	}
	if err := configSupportProblem(declared, old, true); err == nil {
		t.Fatal("a declared ready_timeout was handed to a daemon that ignores it")
	} else if !strings.Contains(err.Error(), "ready_timeout") || !strings.Contains(err.Error(), "daemon restart") {
		t.Fatalf("error = %v, want it to name ready_timeout and the daemon restart", err)
	}
	if err := configSupportProblem(declared, old, false); err != nil {
		t.Fatalf("stopping services does not need ready_timeout support: %v", err)
	}
	if err := configSupportProblem(plain, old, true); err != nil {
		t.Fatalf("a file relying on the 30s default is compatible with an old daemon: %v", err)
	}

	updated := old
	updated.DaemonVersion = "v1.4.0"
	updated.Capabilities = append(append([]string{}, old.Capabilities...), rpc.CapabilityReadyTimeout)
	if err := configSupportProblem(declared, updated, true); err != nil {
		t.Fatalf("a daemon announcing the capability was rejected: %v", err)
	}
}

// configPathForProject is how a named start finds the manifest the daemon will
// read. It matches the project by group name or by repository, and a main
// checkout's manifest speaks for the project over a linked worktree's.
func TestConfigPathForProject(t *testing.T) {
	path := func(s string) *string { return &s }
	for _, tc := range []struct {
		name    string
		groups  []state.Group
		project string
		want    string
	}{
		{
			name: "main checkout",
			groups: []state.Group{
				{Name: "shop", Repo: "shop", ConfigPath: path("/work/shop/oberth.yaml")},
			},
			project: "shop",
			want:    "/work/shop/oberth.yaml",
		},
		{
			name: "main checkout wins over a linked worktree",
			groups: []state.Group{
				{Name: "shop@feature", Repo: "shop", Worktree: "feature", ConfigPath: path("/work/shop-feature/oberth.yaml")},
				{Name: "shop", Repo: "shop", ConfigPath: path("/work/shop/oberth.yaml")},
			},
			project: "shop",
			want:    "/work/shop/oberth.yaml",
		},
		{
			name: "named linked worktree resolves to itself",
			groups: []state.Group{
				{Name: "shop", Repo: "shop", ConfigPath: path("/work/shop/oberth.yaml")},
				{Name: "shop@feature", Repo: "shop", Worktree: "feature", ConfigPath: path("/work/shop-feature/oberth.yaml")},
			},
			project: "shop@feature",
			want:    "/work/shop-feature/oberth.yaml",
		},
		{
			name: "matched by repo when the group is named per worktree",
			groups: []state.Group{
				{Name: "shop@a", Repo: "shop", Worktree: "a", ConfigPath: path("/work/a/oberth.yaml")},
				{Name: "shop@b", Repo: "shop", Worktree: "b", ConfigPath: path("/work/b/oberth.yaml")},
			},
			project: "shop",
			want:    "/work/a/oberth.yaml",
		},
		{
			name: "no manifest path",
			groups: []state.Group{
				{Name: "shop", Repo: "shop"},
			},
			project: "shop",
			want:    "",
		},
		{
			name:    "no such project",
			groups:  []state.Group{{Name: "other", Repo: "other", ConfigPath: path("/work/other/oberth.yaml")}},
			project: "shop",
			want:    "",
		},
		{
			name:    "blank name",
			groups:  []state.Group{{Name: "shop", Repo: "shop", ConfigPath: path("/work/shop/oberth.yaml")}},
			project: "  ",
			want:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := configPathForProject(tc.groups, tc.project); got != tc.want {
				t.Fatalf("configPathForProject = %q, want %q", got, tc.want)
			}
		})
	}
}

// requireNamedProjectSupport is a no-op wherever it has nothing it can judge:
// no client and no project name are the two cases a pure test can pin without a
// daemon. What it does with a real snapshot is the same configSupportProblem
// rule tested above; only the path lookup is this function's own logic, and
// configPathForProject covers that.
func TestRequireNamedProjectSupportIsANoOpWithoutClientOrName(t *testing.T) {
	ctx := context.Background()
	if err := requireNamedProjectSupport(ctx, nil, rpc.GroupsStartParams{}); err != nil {
		t.Fatalf("nil client = %v, want nil", err)
	}
	blank := "  "
	if err := requireNamedProjectSupport(ctx, nil, rpc.GroupsStartParams{Name: &blank}); err != nil {
		t.Fatalf("nil client with a name = %v, want nil", err)
	}
}
