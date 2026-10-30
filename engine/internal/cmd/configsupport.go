package cmd

import (
	"fmt"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// requireConfigSupport refuses to hand a file to a daemon older than the
// format it uses.
//
// The daemon parses `oberth.yaml` itself, so one that predates `port: auto`
// answers with "cannot unmarshal !!str `auto` into int" — an error about the
// file, for a problem with the daemon. This says the true thing instead, and
// what to do about it. A daemon from before capabilities were announced at all
// is treated the same way, which is right: it is older still.
func requireConfigSupport(c *client.Client, cfg *groups.Config) error {
	return requireConfigSupportFor(c, cfg, true)
}

// requireStopConfigSupport is the stricter check for a command that only
// stops services. `env_file:` and `workspace:` change how a service starts, so
// an older daemon must not be handed a file that uses them; neither is needed
// to stop the services it already started.
func requireStopConfigSupport(c *client.Client, cfg *groups.Config) error {
	return requireConfigSupportFor(c, cfg, false)
}

func requireConfigSupportFor(c *client.Client, cfg *groups.Config, starting bool) error {
	if cfg == nil {
		return nil
	}
	hello := c.Hello()
	if cfg.UsesAssignedPorts() && !hasCapability(hello, rpc.CapabilityAutoPorts) {
		return unsupportedConfig(cfg, hello, "`port: auto`")
	}
	if starting && cfg.UsesManifestExtras() && !hasCapability(hello, rpc.CapabilityManifestExtras) {
		return unsupportedConfig(cfg, hello, "`env_file:` or `workspace:`")
	}
	return nil
}

func hasCapability(hello rpc.DaemonHelloResult, want string) bool {
	for _, capability := range hello.Capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func unsupportedConfig(cfg *groups.Config, hello rpc.DaemonHelloResult, feature string) error {
	version := hello.DaemonVersion
	if version == "" {
		version = "an older version"
	}
	return fmt.Errorf("%s uses %s, which the running daemon (%s) does not know\nhint: restart it with `oberth daemon restart`",
		shortPath(cfg.Path), feature, version)
}
