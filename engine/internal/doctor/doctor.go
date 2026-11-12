// Package doctor runs option-berth's self-diagnosis: one table of independent
// questions, each answered with a status, a one-line finding and, where it
// applies, a repair step.
//
// `oberth doctor` and the `daemon.doctor` RPC execute the same table, so the
// desktop app reads the same rows without shelling out to a CLI it may not have
// found. A question about the invoking process — which binary PATH resolves,
// which build it is — still gets a row when the daemon answers; it comes back
// as `skip` with the reason instead of disappearing, so the table never changes
// shape depending on who answered.
package doctor

import (
	"context"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// The four verdicts a check can return, in the order a reader should worry
// about them.
const (
	// StatusOK means the check found what it expected.
	StatusOK = "ok"
	// StatusWarn means the state works but is not what this build expects.
	StatusWarn = "warn"
	// StatusFail means something a person is about to rely on is broken.
	StatusFail = "fail"
	// StatusSkip means the check did not apply, and detail says why.
	StatusSkip = "skip"
)

// Mode says which process is running the checks. It only changes what the
// process-only checks report, never the list of checks: a caller that asked for
// `cli_on_path` gets a row for it either way, so a client's table does not
// change shape depending on who answered.
type Mode string

const (
	// ModeCLI is a `oberth doctor` run in the CLI process.
	ModeCLI Mode = "cli"
	// ModeDaemon is a `daemon.doctor` call answered inside the daemon.
	ModeDaemon Mode = "daemon"
)

// cliOnlyDetail is what a process-only check says when the daemon answered.
const cliOnlyDetail = "CLI-only: this check is about the option-berth binary you invoked, " +
	"which the daemon cannot see from its own process. Run `oberth doctor` for it."

// DaemonInfo is what one daemon probe established. The CLI fills it by
// dialling; the daemon fills it from its own runtime. That is why this is a
// plain value and not a client handle.
type DaemonInfo struct {
	// Reachable is false when nothing answered on the socket; Err then says why.
	Reachable bool
	Err       error
	// Local marks an answer produced inside the daemon process itself.
	Local bool

	// Commit is the build identity the daemon reports, when it has one.
	Commit string
	// StatusErr is why the daemon could not answer a status call.
	StatusErr error
	// Version is the daemon's own version string.
	Version string
	// ProtocolVersion is the protocol the daemon speaks.
	ProtocolVersion string
	// Socket is the address it listens on.
	Socket string
	// PID is its process id.
	PID int
	// DBPath is the database it opened, when it has one.
	DBPath string
	// Observation is the daemon's own listener self-probe, when it reports one.
	Observation *rpc.ObservationStatus
}

// Env is the slice of the world the checks read. Anything a test would
// otherwise have to fake on the real machine — the executable's own path, what
// PATH resolves, the network, docker — is a seam here. Plain files are not:
// every test binary already runs with an isolated HOME (internal/testenv), and
// reading a real temp file is more honest than faking os.ReadFile.
type Env struct {
	// SettingsPath is the owner-only attn.json for this installation.
	SettingsPath string
	// Mode is who is running the checks.
	Mode Mode
	// GOOS overrides runtime.GOOS so the Windows branches can be exercised on
	// any host.
	GOOS string
	// Version and Commit identify the running process.
	Version string
	Commit  string
	// Project is the directory the project-scoped checks look at. Empty means
	// the process's working directory.
	Project string

	// Home is the user's home directory; the paths below mirror how this
	// installation resolves them.
	Home       string
	ConfigPath string
	ConfigDir  string
	SocketPath string
	DBPath     string

	// Executable resolves the running binary's own path. os.Executable is the
	// real implementation; a test substitutes its own.
	Executable func() (string, error)
	// LookPath resolves a command name against PATH.
	LookPath func(string) (string, error)
	// UID is the current user's numeric id on unix; -1 when unknown. The
	// socket check compares it with the socket file's owner. uidSet records
	// that the caller supplied UID, so an explicit 0 (root) survives resolve.
	UID    int
	uidSet bool

	// Daemon reports the daemon's identity, or why it could not be reached.
	Daemon func(context.Context) DaemonInfo
	// Docker reports the docker daemon's server version, or an error when the
	// CLI is there but its daemon is not answering.
	Docker func(context.Context) (string, error)
	// ProbeObservation binds a real local port and asks the collector whether
	// it can see it as this process.
	ProbeObservation func(context.Context) ports.ProbeResult
}

// scope records who is able to answer a check. The zero value means either
// process can answer from what it observes.
type scope uint8

// scopeCLI marks a check only the invoking CLI process holds the fact for.
const scopeCLI scope = 1

// check is one row of the table: a stable id, who can answer it, and the
// question itself. ask assumes env.resolve() has run; Run is the only
// production entry point that guarantees it.
type check struct {
	id    string
	scope scope
	ask   func(context.Context, *Env) rpc.DoctorCheck
}

// catalog is the table, in print order: the binary, then the daemon, then its
// storage and this process's listener observation, then the integrations, then
// the project, and last the optional extras.
var catalog = []check{
	{id: "cli_on_path", scope: scopeCLI, ask: checkCLIOnPath},
	{id: "config_parses", ask: checkConfigParses},
	{id: "config_dir_writable", ask: checkConfigDirWritable},
	{id: "daemon_reachable", ask: checkDaemonReachable},
	{id: "daemon_version_matches", scope: scopeCLI, ask: checkDaemonVersionMatches},
	{id: "daemon_build_matches", scope: scopeCLI, ask: checkDaemonBuildMatches},
	{id: "daemon_protocol", ask: checkDaemonProtocol},
	{id: "socket_permissions", ask: checkSocketPermissions},
	{id: "db_ok", ask: checkDBOK},
	{id: "listener_observation", ask: checkListenerObservation},
	{id: "project_config", ask: checkProjectConfig},
	{id: "docker", ask: checkDocker},
	// No row reports agent wiring: option-berth registers no MCP server, hook
	// or another tool's config. The skill row only reads what a user already
	// installed, and a single copy is never called outdated.
	{id: "agent_skill", ask: checkSkillVersion},
	// The optional assessment goes last: it is enrichment, and its row must
	// never push a fact or a control check off the table.
	{id: "attn", ask: checkAttn},
}

// IDs lists every check id, in table order. `oberth doctor --only` validates a
// selector against it.
func IDs() []string {
	ids := make([]string, len(catalog))
	for i, c := range catalog {
		ids[i] = c.id
	}
	return ids
}

// Run executes the selected checks and assembles the reply both `oberth doctor`
// and `daemon.doctor` return.
func Run(ctx context.Context, env Env, only []string) rpc.DaemonDoctorResult {
	env.resolve()
	selected := newSelection(only)

	// The daemon is dialled at most once per run, however many checks ask about
	// it. A run that selects only project checks never dials at all.
	probe := once(env.Daemon)
	env.Daemon = probe.ask

	result := rpc.DaemonDoctorResult{
		OK:      true,
		Checks:  make([]rpc.DoctorCheck, 0, len(catalog)),
		Version: env.Version,
	}
	for _, c := range catalog {
		if !selected.includes(c.id) {
			continue
		}
		row := answer(ctx, &env, c)
		if row.Status == StatusFail {
			result.OK = false
		}
		result.Checks = append(result.Checks, row)
	}
	if reached, asked := probe.last(); asked && reached.Reachable {
		result.DaemonVersion = reached.Version
	}
	return result
}

// answer runs one check, or reports why this process cannot run it. The id is
// set here so no check can drift from the table.
func answer(ctx context.Context, env *Env, c check) rpc.DoctorCheck {
	if c.scope == scopeCLI && env.Mode == ModeDaemon {
		return rpc.DoctorCheck{
			ID:      c.id,
			Status:  StatusSkip,
			Summary: "not checked from the daemon",
			Detail:  cliOnlyDetail,
		}
	}
	row := c.ask(ctx, env)
	row.ID = c.id
	return row
}

// probeCache remembers one answer so a run asks the daemon once. last reports
// whether anything was asked at all, which decides whether the result carries
// the daemon's version.
func once(probe func(context.Context) DaemonInfo) *probeCache {
	return &probeCache{probe: probe}
}

type probeCache struct {
	probe  func(context.Context) DaemonInfo
	answer DaemonInfo
	asked  bool
}

func (c *probeCache) ask(ctx context.Context) DaemonInfo {
	if !c.asked {
		c.answer, c.asked = c.probe(ctx), true
	}
	return c.answer
}

func (c *probeCache) last() (DaemonInfo, bool) { return c.answer, c.asked }
