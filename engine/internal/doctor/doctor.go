// Package doctor runs option-berth's self-diagnosis: one list of independent checks
// that say whether the CLI, the daemon, the database, the agent integrations
// and this project's config are in the state option-berth expects.
//
// The same list backs `oberth doctor` and the `daemon.doctor` RPC the desktop
// app calls, so the app never has to shell out to learn what is wrong. A few
// checks are about the CLI process itself (which binary PATH resolves, how it
// compares with the daemon and with the latest release); those the daemon
// cannot answer from its own process and reports as `skip`, saying so in the
// check's detail.
package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// Status values, in the order a reader should worry about them.
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
	StatusSkip = "skip"
)

// Mode says which process is running the checks. It only changes what the
// CLI-only checks report, never the list of checks: a caller that asked for
// `cli_on_path` gets a row for it either way, so a client's table does not
// change shape depending on who answered.
type Mode string

const (
	// ModeCLI is a `oberth doctor` run in the CLI process.
	ModeCLI Mode = "cli"
	// ModeDaemon is a `daemon.doctor` call answered inside the daemon.
	ModeDaemon Mode = "daemon"
)

// cliOnlyDetail is what a CLI-only check says when the daemon answered.
const cliOnlyDetail = "CLI-only: this check is about the option-berth binary you invoked, " +
	"which the daemon cannot see from its own process. Run `oberth doctor` for it."

// DaemonInfo is what the doctor knows about the daemon. The CLI fills it in by
// dialling; the daemon fills it in from its own runtime, which is also why
// this is a plain struct rather than a client handle.
type DaemonInfo struct {
	// Reachable is false when nothing answered on the socket.
	Reachable bool
	// Err is why it was not reachable.
	Err error
	// Local is true when the answer came from inside the daemon process.
	Local bool

	Commit          string
	StatusErr       error
	Version         string
	ProtocolVersion string
	Socket          string
	PID             int
	DBPath          string
	// Observation is the daemon's own self-probe verdict, when it reports one.
	Observation *rpc.ObservationStatus
}

// Env is the slice of the world the checks read. Everything that a test would
// otherwise have to fake on the real machine — the executable's own path, what
// PATH resolves, the network, docker — is a field here; plain files are not,
// because every test binary already runs with an isolated HOME (internal/testenv)
// and reading a real temp file is more honest than faking os.ReadFile.
type Env struct {
	// SettingsPath is the owner-only jev.json for this installation. It is a
	// field rather than a global lookup so a check describes this home, not
	// the process's own.
	SettingsPath string
	// Mode is who is running the checks.
	Mode Mode
	// GOOS overrides runtime.GOOS, so the Windows skips can be exercised on
	// any host.
	GOOS string
	// Version is the running process's own version string.
	Version string
	Commit  string
	// Project is the directory the project-scoped checks look at. Empty means
	// the process's working directory.
	Project string

	// Home is the user's home directory.
	Home string
	// ConfigPath is the user's config.yaml.
	ConfigPath string
	// ConfigDir is ~/.option-berth.
	ConfigDir string
	// SocketPath is the address the daemon listens on.
	SocketPath string
	// DBPath is the SQLite database file.
	DBPath string

	// Executable is the running binary's path.
	Executable func() (string, error)
	// LookPath resolves a name against PATH.
	LookPath func(string) (string, error)
	// UID is the current user's numeric id on unix; -1 when unknown.
	UID int

	// Daemon reports the daemon's identity, or why it could not be reached.
	Daemon func(context.Context) DaemonInfo
	// Docker reports the docker daemon's server version, or an error when the
	// CLI is there but the daemon is not answering.
	Docker func(context.Context) (string, error)
	// ProbeObservation binds a real local port and asks the collector whether
	// it can see it as this process. A sandbox that lets the binary run but
	// denies system introspection fails here; that failure is what the check
	// exists to show.
	ProbeObservation func(context.Context) ports.ProbeResult
}

// check is one diagnostic: its id, whether only the CLI can answer it, and the
// function that looks.
type check struct {
	id      string
	cliOnly bool
	run     func(context.Context, *Env) rpc.DoctorCheck
}

// checks is the whole list, in the order the table prints them: the binary,
// then the daemon, then its storage and this process's listener observation,
// then the integrations, then the project, then the optional extras.
func checks() []check {
	list := []check{
		{id: "cli_on_path", cliOnly: true, run: checkCLIOnPath},
		{id: "config_parses", run: checkConfigParses},
		{id: "config_dir_writable", run: checkConfigDirWritable},
		{id: "daemon_reachable", run: checkDaemonReachable},
		{id: "daemon_version_matches", cliOnly: true, run: checkDaemonVersionMatches},
		{id: "daemon_build_matches", cliOnly: true, run: checkDaemonBuildMatches},
		{id: "daemon_protocol", run: checkDaemonProtocol},
		{id: "socket_permissions", run: checkSocketPermissions},
		{id: "db_ok", run: checkDBOK},
		{id: "listener_observation", run: checkListenerObservation},
	}
	// No rows about agent integration in the sense of wiring: option-berth does
	// not register MCP servers, install hooks or write another tool's config.
	// The skill row below reads only what a user already installed, and it says
	// nothing about a single copy being current — see checkSkillVersion.
	return append(list,
		check{id: "project_config", run: checkProjectConfig},
		check{id: "docker", run: checkDocker},
		check{id: "agent_skill", run: checkSkillVersion},
		// The optional assessment goes last: it is enrichment, and its row
		// must never push a fact or a control check off the table.
		check{id: "jev", run: checkJev},
	)
}

// IDs lists every check id, in table order. `oberth doctor --only` validates a
// selector against it.
func IDs() []string {
	all := checks()
	ids := make([]string, 0, len(all))
	for _, c := range all {
		ids = append(ids, c.id)
	}
	return ids
}

// Prefixes lists the selectors `--only` accepts that are not check ids: the
// part before the dot of a dotted id, so a prefix selects every check under it.
func Prefixes() []string {
	seen := map[string]bool{}
	var prefixes []string
	for _, id := range IDs() {
		prefix, _, dotted := strings.Cut(id, ".")
		if !dotted || seen[prefix] {
			continue
		}
		seen[prefix] = true
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	return prefixes
}

// UnknownSelectors returns the entries of only that name no check.
func UnknownSelectors(only []string) []string {
	var bad []string
	for _, sel := range normalize(only) {
		if !selects([]string{sel}, IDs()) {
			bad = append(bad, sel)
		}
	}
	return bad
}

// selects reports whether any of ids is picked by only.
func selects(only, ids []string) bool {
	for _, id := range ids {
		if picked(only, id) {
			return true
		}
	}
	return false
}

// picked reports whether only selects id. An empty only selects everything.
func picked(only []string, id string) bool {
	if len(only) == 0 {
		return true
	}
	for _, sel := range only {
		if sel == id || strings.HasPrefix(id, sel+".") {
			return true
		}
	}
	return false
}

func normalize(only []string) []string {
	out := make([]string, 0, len(only))
	for _, sel := range only {
		for _, part := range strings.Split(sel, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// Run executes the selected checks and assembles the reply both `oberth doctor`
// and `daemon.doctor` return.
func Run(ctx context.Context, env Env, only []string) rpc.DaemonDoctorResult {
	env.fill()
	sel := normalize(only)

	// The daemon is dialled at most once per run, however many checks ask about
	// it. A run that selects only project checks never dials at all.
	ask := once(env.Daemon)
	env.Daemon = ask.ask

	result := rpc.DaemonDoctorResult{OK: true, Checks: []rpc.DoctorCheck{}, Version: env.Version}
	for _, c := range checks() {
		if !picked(sel, c.id) {
			continue
		}
		var got rpc.DoctorCheck
		if c.cliOnly && env.Mode == ModeDaemon {
			got = rpc.DoctorCheck{
				ID:      c.id,
				Status:  StatusSkip,
				Summary: "not checked from the daemon",
				Detail:  cliOnlyDetail,
			}
		} else {
			got = c.run(ctx, &env)
			got.ID = c.id
		}
		if got.Status == StatusFail {
			result.OK = false
		}
		result.Checks = append(result.Checks, got)
	}
	if reached, ok := ask.last(); ok && reached.Reachable {
		result.DaemonVersion = reached.Version
	}
	return result
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

// fill installs the real world behind every seam the caller left empty.
//
// One rule per field, and the rule says only how to resolve that field. A
// caller that wants the standard run passes nothing; a test that wants a
// fixture passes exactly the field it cares about and leaves the rest to the
// same defaults every other command uses.
func (e *Env) fill() {
	e.fillScalars()
	e.fillPaths()
	e.fillSeams()
}

// fillScalars resolves the facts about this process.
func (e *Env) fillScalars() {
	if e.Mode == "" {
		e.Mode = ModeCLI
	}
	if e.GOOS == "" {
		e.GOOS = runtime.GOOS
	}
	if e.UID == 0 {
		e.UID = os.Getuid()
	}
	if e.Home == "" {
		if home, err := os.UserHomeDir(); err == nil {
			e.Home = home
		}
	}
	if e.Project == "" {
		if wd, err := os.Getwd(); err == nil {
			e.Project = wd
		}
	}
}

// fillPaths resolves the files and the address the checks look at.
func (e *Env) fillPaths() {
	setIfEmpty(&e.ConfigPath, config.Path)
	setIfEmpty(&e.ConfigDir, daemon.ConfigDir)
	setIfEmpty(&e.SocketPath, daemon.SocketPath)
	setIfEmpty(&e.DBPath, store.Path)
	setIfEmpty(&e.SettingsPath, paths.JevConfigPath)
}

// fillSeams resolves the functions a check calls to see something the doctor
// cannot see for itself. A seam nobody wired reports errNoProbe, so the check
// that needs it skips instead of reading an empty result as a fact.
func (e *Env) fillSeams() {
	if e.Executable == nil {
		e.Executable = os.Executable
	}
	if e.LookPath == nil {
		e.LookPath = exec.LookPath
	}
	if e.Daemon == nil {
		e.Daemon = func(context.Context) DaemonInfo { return DaemonInfo{Err: errNoProbe} }
	}
	if e.Docker == nil {
		e.Docker = func(context.Context) (string, error) { return "", errNoProbe }
	}
	if e.ProbeObservation == nil {
		e.ProbeObservation = ports.ProbeListenerObservation
	}
}

// setIfEmpty installs a resolved default only where the caller left the field
// blank.
func setIfEmpty(field *string, resolve func() string) {
	if *field == "" {
		*field = resolve()
	}
}

// errNoProbe is what an unwired seam reports: the caller did not give the
// doctor a way to look, so the check that needs it skips.
var errNoProbe = errors.New("not available in this process")

// gitRootOf is the repository root above dir, or "" when dir is not in one.
func gitRootOf(dir string) string {
	root, _, ok := groups.Find(dir)
	if !ok {
		return ""
	}
	return root
}

// home joins a path under the user's home directory, or "" without one.
func (e *Env) homePath(parts ...string) string {
	if e.Home == "" {
		return ""
	}
	return filepath.Join(append([]string{e.Home}, parts...)...)
}

func exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// ---------------------------------------------------------------------------
// Result constructors.
//
// A check says three things and occasionally two more: what it saw, how bad it
// is, and what to do next. Writing that as a struct literal leaves `Status:`
// and `Summary:` repeated on every branch of every check, which is how a
// reader loses the branch that actually differs. These four name the verdict,
// so the branches read as the sentences they produce.
// ---------------------------------------------------------------------------

// ok is a check that found what it expected.
func ok(summary, detail string) rpc.DoctorCheck {
	return rpc.DoctorCheck{Status: StatusOK, Summary: summary, Detail: detail}
}

// warn is a state that works but is not what this build expects.
func warn(summary, detail, fix string) rpc.DoctorCheck {
	return rpc.DoctorCheck{Status: StatusWarn, Summary: summary, Detail: detail, Fix: fix}
}

// fail is a state that breaks something a person is about to rely on.
func fail(summary, detail, fix string) rpc.DoctorCheck {
	return rpc.DoctorCheck{Status: StatusFail, Summary: summary, Detail: detail, Fix: fix}
}

// skip is a check that did not apply, with the reason a reader needs to decide
// whether it should have. It takes no fix: a skipped check is not a problem to
// repair.
func skip(summary, detail string) rpc.DoctorCheck {
	return rpc.DoctorCheck{Status: StatusSkip, Summary: summary, Detail: detail}
}

// skipWithFix is skip for a check that did not apply *yet* and whose fix is the
// way to make it apply — the optional Jev row, whose fix turns it on.
func skipWithFix(summary, detail, fix string) rpc.DoctorCheck {
	result := skip(summary, detail)
	result.Fix = fix
	return result
}

// The fixable forms carry the same four fields, with the repair flag set: the
// doctor found something it can mend itself, so the CLI may offer to.
func okFixable(summary, detail, fix string) rpc.DoctorCheck {
	return fixable(ok(summary, detail))
}

func warnFixable(summary, detail, fix string) rpc.DoctorCheck {
	return fixable(warn(summary, detail, fix))
}

func failFixable(summary, detail, fix string) rpc.DoctorCheck {
	return fixable(fail(summary, detail, fix))
}

func skipFixable(summary, detail, fix string) rpc.DoctorCheck {
	result := skip(summary, detail)
	result.Fix = fix
	return fixable(result)
}

func fixable(result rpc.DoctorCheck) rpc.DoctorCheck {
	result.Fixable = true
	return result
}
