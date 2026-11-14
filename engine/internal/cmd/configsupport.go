package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// requireNamedProjectSupport runs the same manifest capability check when the
// project was named rather than resolved from the working directory.
//
// `oberth up <project>` and `oberth restart <project>` send a name, so the CLI
// has no manifest to inspect and the check in requireConfigSupport has nothing
// to read (cfg is nil). The runtime service still finds the file itself. Without this,
// a named start against a runtime service that does not honour `ready_timeout:` would
// silently wait its old fixed 30s while the file promises something else.
//
// It only works when the runtime service does not already announce the capability; a
// runtime service that does has nothing to be warned about, and paying for a snapshot
// read on every start would be wrong. A manifest the CLI cannot find or parse
// is not its to judge: the runtime service owns the file and reports its own error, so
// this returns nil and lets that error happen.
func requireNamedProjectSupport(ctx context.Context, c *client.Client, p rpc.GroupsStartParams) error {
	if c == nil || p.Name == nil {
		return nil
	}
	name := strings.TrimSpace(*p.Name)
	if name == "" || hasCapability(c.Hello(), rpc.CapabilityReadyTimeout) {
		return nil
	}
	var snap state.Snapshot
	if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
		// The snapshot is how this check finds the file, not what it is for.
		// A read that fails here leaves the start to the runtime service's own answer.
		return nil
	}
	path := configPathForProject(snap.Groups, name)
	if path == "" {
		return nil
	}
	cfg, err := groups.Load(path)
	if err != nil {
		return nil
	}
	return configSupportProblem(cfg, c.Hello(), true)
}

// configPathForProject picks the manifest a named project resolves to. It
// matches the group name exactly, the same rule the runtime service's own `ConfigNamed`
// uses: a linked worktree's group is `<repo>@<worktree>`, so `up <repo>` does
// not silently select one of its worktrees. A main checkout wins if two groups
// ever carry the same name.
func configPathForProject(gg []state.Group, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	first := ""
	for _, g := range gg {
		if g.Name != name {
			continue
		}
		if g.ConfigPath == nil || *g.ConfigPath == "" {
			continue
		}
		if g.Worktree == "" {
			return *g.ConfigPath
		}
		if first == "" {
			first = *g.ConfigPath
		}
	}
	return first
}

// requireConfigSupport refuses to hand a file to a runtime service older than the
// format it uses.
//
// The runtime service parses `oberth.yaml` itself, so one that predates `port: auto`
// answers with "cannot unmarshal !!str `auto` into int" — an error about the
// file, for a problem with the runtime.service. This says what is actually true, and
// what to do about it. A runtime service from before capabilities were announced at
// all is treated the same way, which is right: it is older still.
func requireConfigSupport(c *client.Client, cfg *groups.Config) error {
	return requireConfigSupportFor(c, cfg, true)
}

// requireStopConfigSupport is the stricter check for a command that only
// stops services. `env_file:` and `workspace:` change how a service starts, so
// an older runtime service must not be handed a file that uses them; neither is needed
// to stop the services it already started.
func requireStopConfigSupport(c *client.Client, cfg *groups.Config) error {
	return requireConfigSupportFor(c, cfg, false)
}

func requireConfigSupportFor(c *client.Client, cfg *groups.Config, starting bool) error {
	if cfg == nil {
		return nil
	}
	return configSupportProblem(cfg, c.Hello(), starting)
}

// configSupportProblem is the capability check without a live client, so the
// decisions are testable without a runtime.service. `ready_timeout:` is checked only
// for commands that start services: stopping what is already running does not
// need the runtime service to honour a readiness budget.
func configSupportProblem(cfg *groups.Config, hello rpc.RuntimeServiceHelloResult, starting bool) error {
	if cfg.UsesAssignedPorts() && !hasCapability(hello, rpc.CapabilityAutoPorts) {
		return unsupportedConfig(cfg, hello, "`port: auto`")
	}
	if starting && cfg.UsesManifestExtras() && !hasCapability(hello, rpc.CapabilityManifestExtras) {
		return unsupportedConfig(cfg, hello, "`env_file:` or `workspace:`")
	}
	if starting && cfg.UsesReadyTimeout() && !hasCapability(hello, rpc.CapabilityReadyTimeout) {
		return unsupportedConfig(cfg, hello, "`ready_timeout:`")
	}
	return nil
}

// requireStartDependencySupport refuses to send include_dependencies to a
// runtime service that predates it. The old runtime service ignores the field and starts only the
// named services, so the caller would get a partial group after the usual
// timeout for the services that never came up — an error about the services
// for a problem with the runtime.service. Restarting it, or asking for exactly what was
// named, are both real fixes.
func requireStartDependencySupport(c *client.Client, include bool) error {
	return requireStartDependencySupportFor(c.Hello(), include)
}

func requireStartDependencySupportFor(hello rpc.RuntimeServiceHelloResult, include bool) error {
	if !include || hasCapability(hello, rpc.CapabilityStartDependencies) {
		return nil
	}
	version := hello.RuntimeServiceVersion
	if version == "" {
		version = "an older version"
	}
	return fmt.Errorf("the running runtime service (%s) cannot start a service's depends_on closure\nhint: restart it with `oberth runtime service restart`, or pass `--no-deps`",
		version)
}

func hasCapability(hello rpc.RuntimeServiceHelloResult, want string) bool {
	for _, capability := range hello.Capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func unsupportedConfig(cfg *groups.Config, hello rpc.RuntimeServiceHelloResult, feature string) error {
	version := hello.RuntimeServiceVersion
	if version == "" {
		version = "an older version"
	}
	return fmt.Errorf("%s uses %s, which the running runtime service (%s) does not know\nhint: restart it with `oberth runtime service restart`",
		shortPath(cfg.Path), feature, version)
}
