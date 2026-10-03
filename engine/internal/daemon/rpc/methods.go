package rpc

import (
	"encoding/json"

	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// This file declares the params and results of every daemon method named in
// the daemon spec's method table and in cross-spec contract §4. In slice F0
// they are placeholders: they fix the wire shape and drive schema generation,
// and the slices that implement each method fill in the behaviour behind them.
// Changing a field here changes the published protocol, so keep it in step
// with the contract.

// ---------------------------------------------------------------- shared ---

// HostParams is retained as the local-only selector envelope used by the
// existing method shapes. Its zero value addresses this daemon.
//
// The daemon accepts it on every method it serves except `state.*`, `stream.*`,
// `daemon.hello` and `daemon.shutdown`. It is embedded in the params types
// below where the wire shape already carries it.
type HostParams struct {
	// Host names a registered remote host. Empty or "localhost" is this
	// machine.
	Host string `json:"host,omitempty"`
}

// Selector addresses one port (contract §3). Exactly one of Port, PID, RunID
// and Key is set; BindAddress only disambiguates Port.
type Selector struct {
	HostParams
	Port        *int    `json:"port,omitempty"`
	PID         *int    `json:"pid,omitempty"`
	BindAddress *string `json:"bind_address,omitempty"`
	RunID       *string `json:"run_id,omitempty"`
	// Key is a delta key handed straight back as a selector: `"<port>"`,
	// `"<port>:<bind_address>"`, or either of those behind a `"<host>/"`
	// prefix naming a registered host. A client that holds the key the stream
	// gave it does not have to take it apart to act on the row. The daemon
	// expands it into Host, Port and BindAddress before any handler sees it,
	// so it never combines with them.
	Key string `json:"key,omitempty"`
}

// MutationResult is the minimum every mutating method returns (contract §3).
// Affected holds port keys ("<port>:<bind_address>").
type MutationResult struct {
	OK       bool     `json:"ok"`
	Affected []string `json:"affected"`
}

// KillEnvelope is the result of every kill-shaped method (contract §3). The row
// type is state.KillResult, the single Go type the killer, the CLI and the
// daemon all produce, so it keeps the plain name in the generated schema
// (contract §17) and the envelope around it is named for what it is.
type KillEnvelope struct {
	MutationResult
	Results []state.KillResult `json:"results"`
	// Released is how many claimed ports a `groups.kill` with release gave
	// back (`oberth down`). Zero for every other kill.
	Released int `json:"released,omitempty"`
}

// Empty is the params or result of a method that takes or returns nothing.
type Empty struct{}

// OKResult is a bare acknowledgement.
type OKResult struct {
	OK bool `json:"ok"`
}

// CapabilityAutoPorts is announced by a daemon that understands the config
// format v0.8.0 introduced: `port: auto`, `env:` and ${…} references. A client
// holding such a file checks for it before handing the file over, because an
// older daemon parses it itself and reports it as invalid — blaming the file
// for the daemon's age.
const CapabilityAutoPorts = "groups.autoports"

// CapabilityStateScope is announced by daemons that understand scoped state
// snapshots and the redacted state.changed notification.
const CapabilityStateScope = "state.scope"

// Include lists the optional per-subscriber enrichments ("stats", "health").
type Include []string

// ---------------------------------------------------------------- daemon ---

type DaemonHelloParams struct {
	Client        string `json:"client"` // cli | app | mcp | tray
	ClientVersion string `json:"client_version"`
	Keepalive     bool   `json:"keepalive,omitempty"`
}

type DaemonHelloResult struct {
	ProtocolVersion string   `json:"protocol_version"`
	DaemonVersion   string   `json:"daemon_version"`
	PID             int      `json:"pid"`
	StartedAt       string   `json:"started_at"`
	Capabilities    []string `json:"capabilities"`
	Socket          string   `json:"socket"`
	BinaryPath      string   `json:"binary_path"`
	Keepalive       bool     `json:"keepalive"`
}

type DaemonStatusResult struct {
	PID         int    `json:"pid"`
	Uptime      string `json:"uptime"`
	Subscribers int    `json:"subscribers"`
	LastScanAt  string `json:"last_scan_at"`
	// Commit and Built are what this daemon was compiled from. A daemon is
	// started once and keeps running, so after any change to the engine it is
	// answering from the previous build — and a reader comparing these with its
	// own is the only way to see that. The CLI does exactly that.
	Commit string `json:"commit"`
	Built  string `json:"built"`
	// ScanIntervalMs is the adaptive port-scan cadence right now; it moves
	// between the base and a ceiling as scans come back unchanged.
	ScanIntervalMs int `json:"scan_interval_ms"`
	// ScanBaseIntervalMs and StatsIntervalMs are the effective settings
	// behind it: `daemon.scan_interval` and `daemon.stats_interval` as this
	// daemon resolved them at startup. Both are read once, so a config edit
	// needs a daemon restart and these are how you check that it took.
	ScanBaseIntervalMs int `json:"scan_base_interval_ms"`
	StatsIntervalMs    int `json:"stats_interval_ms"`
	// Scans counts the port scans this daemon has run. Two clients reading
	// through the daemon must not make it grow faster than one does.
	Scans  int64  `json:"scans"`
	DBPath string `json:"db_path"`
}

// DaemonSchemaResult is the JSON Schema bundle this package generates.
type DaemonSchemaResult struct {
	Schema json.RawMessage `json:"schema"`
}

// DoctorCheck is one diagnostic `oberth doctor` and `daemon.doctor` report. It
// is the single Go type behind the table, the CLI's `--json` and the wire
// result (contract §17), so a client that renders one renders the other.
type DoctorCheck struct {
	// ID names the check. Checks that exist once per external tool are
	// dotted: `mcp_registered.claude_code`.
	ID string `json:"id"`
	// Status is ok, warn, fail or skip. Only `fail` makes the run not ok.
	Status string `json:"status"`
	// Summary is the one line a table row shows.
	Summary string `json:"summary"`
	// Detail is the evidence: paths, versions, the parse error. It is also
	// where a check the daemon cannot run from its own process says so.
	Detail string `json:"detail,omitempty"`
	// Fix is the human hint: what to run, or what to edit.
	Fix string `json:"fix,omitempty"`
	// Fixable says `oberth doctor --fix` can repair this one unattended.
	Fixable bool `json:"fixable"`
}

type DaemonDoctorParams struct {
	HostParams
	// Only keeps just these checks. An entry matches a check id exactly, or
	// every check under it when the id is dotted (`mcp_registered`).
	Only []string `json:"only,omitempty"`
	// Project is the directory the project-scoped checks look at. Empty means
	// the process's own working directory, which for the daemon is wherever it
	// was started, so a client that cares always sends it.
	Project string `json:"project,omitempty"`
}

// DaemonDoctorResult is what both `oberth doctor --json` and `daemon.doctor`
// return. OK is false when any check failed.
type DaemonDoctorResult struct {
	OK     bool          `json:"ok"`
	Checks []DoctorCheck `json:"checks"`
	// Version is the version of the process that ran the checks: the CLI for
	// `oberth doctor`, the daemon for `daemon.doctor`.
	Version string `json:"version"`
	// DaemonVersion is the daemon's version, empty when it is not reachable.
	DaemonVersion string `json:"daemon_version"`
}

// ----------------------------------------------------------------- state ---

type StateSnapshotParams struct {
	Include Include `json:"include,omitempty"`
	// Scope is carried on the wire as filter. Nil preserves the historical
	// machine-wide snapshot; a scoped request returns the redacted stream
	// projection rather than command/config details.
	Scope *state.Scope `json:"filter,omitempty"`
}

// StateSnapshotResult is the additive result envelope. Legacy callers receive
// the original collections; scoped callers receive only StreamSnapshot fields.
// Keeping one top-level shape lets older clients continue decoding the legacy
// snapshot while new clients can opt into the safe projection.
type StateSnapshotResult struct {
	// Legacy state.snapshot fields.
	Seq           uint64                `json:"seq,omitempty"`
	At            string                `json:"at,omitempty"`
	DaemonVersion string                `json:"daemon_version,omitempty"`
	Ports         []state.Port          `json:"ports,omitempty"`
	Groups        []state.Group         `json:"groups,omitempty"`
	Sessions      []state.SessionRecord `json:"sessions,omitempty"`

	// Scoped stream fields.
	Type          string                 `json:"type,omitempty"`
	StateRevision string                 `json:"state_revision,omitempty"`
	ObservedAt    string                 `json:"observed_at,omitempty"`
	Worktrees     []state.StreamWorktree `json:"worktrees,omitempty"`
}

func (r StateSnapshotResult) Legacy() state.Snapshot {
	return state.Snapshot{Seq: r.Seq, At: r.At, DaemonVersion: r.DaemonVersion, Ports: r.Ports, Groups: r.Groups, Sessions: r.Sessions}
}

func (r StateSnapshotResult) Stream() state.StreamSnapshot {
	return state.StreamSnapshot{Type: r.Type, Seq: r.Seq, StateRevision: r.StateRevision, ObservedAt: r.ObservedAt, Worktrees: r.Worktrees}
}

type StateSubscribeParams struct {
	Include Include `json:"include,omitempty"`
	Events  bool    `json:"events,omitempty"`
	// AfterSeq asks the daemon to resume from this published sequence. When
	// the in-memory replay window no longer covers it, subscribe safely falls
	// back to a fresh snapshot.
	AfterSeq uint64 `json:"after_seq,omitempty"`
	// Scope is carried on the wire as filter. Nil preserves the historical
	// machine-wide subscription for existing clients; scoped subscriptions use
	// the redacted state.changed stream and must set Events.
	Scope *state.Scope `json:"filter,omitempty"`
}

// ----------------------------------------------------------------- ports ---

type PortsKillParams struct {
	HostParams
	Targets  []Selector `json:"targets"`
	Tree     bool       `json:"tree,omitempty"`
	Force    bool       `json:"force,omitempty"`
	GraceMs  int        `json:"grace_ms,omitempty"`
	Escalate *bool      `json:"escalate,omitempty"`
	DryRun   bool       `json:"dry_run,omitempty"`
}

type PortsLogsParams struct {
	Selector
	Lines  int  `json:"lines,omitempty"`
	Follow bool `json:"follow,omitempty"`
}

// PortsLogsResult is the unary reply (follow: false). With follow: true the
// method also returns a subscription_id and pushes PortsLogsChunk.
type PortsLogsResult struct {
	Source         string   `json:"source"`
	Lines          []string `json:"lines"`
	Truncated      bool     `json:"truncated"`
	SubscriptionID string   `json:"subscription_id,omitempty"`
}

type PortsLogsChunk struct {
	Source string `json:"source"`
	Line   string `json:"line"`
}

// ---------------------------------------------------------------- groups ---

type GroupsListResult struct {
	Groups []state.Group `json:"groups"`
}

type GroupsKillParams struct {
	HostParams
	Name string `json:"name"`
	// ConfigPath names the group by its oberth.yaml instead of by name, for a
	// caller that knows the file but not the name the daemon publishes its
	// group under (`<project>@<worktree>` in a linked worktree, an alias).
	ConfigPath *string `json:"config_path,omitempty"`
	Force      bool    `json:"force,omitempty"`
	GraceMs    int     `json:"grace_ms,omitempty"`
	DryRun     bool    `json:"dry_run,omitempty"`
	// Only narrows a project stop to these declared services. It is used by
	// `oberth restart --only`; scoped stops keep auto-port claims.
	Only []string `json:"only,omitempty"`
	// Release is `oberth down`: besides the group's listening ports it stops
	// every run option-berth started in the group, port or not, and releases the
	// claims the group's `port: auto` services hold. A group with a config
	// and nothing running is not an error then: its claims are still released.
	Release bool `json:"release,omitempty"`
	// Reason lets a higher-level lifecycle operation preserve its terminal
	// outcome. Ordinary stops leave this empty and are recorded as "stopped";
	// `up --wait` uses "ready_timeout" so status, attention and events agree.
	Reason string `json:"reason,omitempty" jsonschema:"enum=stopped,enum=ready_timeout"`
}

type GroupsStartParams struct {
	HostParams
	Name             *string  `json:"name,omitempty"`
	ConfigPath       *string  `json:"config_path,omitempty"`
	Only             []string `json:"only,omitempty"`
	AllowOutsideHome bool     `json:"allow_outside_home,omitempty"`
	// Env is the environment the services start in, layered over the
	// daemon's own. The CLI sends its shell's, so a service sees the PATH,
	// toolchain and virtualenv it was started from rather than the daemon's.
	// Omitted, the services get the daemon's environment.
	Env map[string]string `json:"env,omitempty"`
}

type GroupsStartResult struct {
	MutationResult
	SubscriptionID string `json:"subscription_id"`
	// StartID is shared by every run this call starts, so the services
	// brought up together can be told apart from the ones already running.
	StartID string `json:"start_id,omitempty"`
}

// GroupsStartChunk is one service's outcome, pushed as it happens: it was
// started (pid and log_path), it was skipped (with the reason), or it could not
// be started (error).
type GroupsStartChunk struct {
	Service string `json:"service"`
	// State is the machine-readable outcome for this service: started,
	// skipped, failed, or ready_timeout (when the CLI was asked to wait).
	State string `json:"state,omitempty" jsonschema:"enum=started,enum=skipped,enum=failed,enum=ready_timeout"`
	PID   int    `json:"pid,omitempty"`
	// Port is the port a started service was told to bind: its fixed port,
	// or the one assigned for `port: auto`. Zero for a service with none.
	Port    int    `json:"port,omitempty"`
	LogPath string `json:"log_path,omitempty"`
	// RunID is the run a started service became: the id `runs.list` reports
	// it under and `ports.kill {run_id}` stops it by.
	RunID string `json:"run_id,omitempty"`
	// LogOffset is how long log_path already was before this start. The file
	// is appended to across runs, so a client following it starts here to
	// show this run and not the ones before.
	LogOffset int64  `json:"log_offset,omitempty"`
	Skipped   bool   `json:"skipped,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
	// Hint is a deterministic next command for a failed service. It contains
	// no model judgement; it points at the run evidence option-berth already
	// collected.
	Hint string `json:"hint,omitempty"`
}

type GroupsStartEnd struct {
	Started []string `json:"started"`
	Skipped []string `json:"skipped"`
	Errors  []string `json:"errors"`
}

// GroupsRenameParams renames a project (step 5A.6). Name is the project's name
// or the name of any of its checkout groups: the rename always applies to the
// project, so the main checkout's group becomes To and every linked worktree's
// group becomes `<To>@<worktree>`. A project whose main checkout has a
// `oberth.yaml` is renamed by writing `name:` into that file; one without keeps
// the new name in the daemon.
//
// Errors: invalid_params for an empty name or a To that is empty or holds `@`,
// `/` or whitespace, and for a group whose name is not a project's to change —
// a group a `oberth start --group` named, or a Compose project; not_found
// for an unknown group; conflict when a group outside the project already has a
// name the rename would give; invalid_config when the edited file would no
// longer validate.
type GroupsRenameParams struct {
	HostParams
	Name string `json:"name"`
	To   string `json:"to"`
}

// GroupsRenameResult carries in Affected the new name of every group the rename
// changed — the project's and each checkout's — sorted, and in Name the
// project's new name. Affected is empty when the project already had that name.
type GroupsRenameResult struct {
	MutationResult
	Name string `json:"name"`
}

// GroupConfig is a `oberth.yaml` as the protocol carries it: the group name,
// the services as contract rows, and the extra ports the file claims. It is
// the `config` of groups.config.get and groups.config.set (contract §13.2).
type GroupConfig struct {
	Name     string          `json:"name"`
	Services []state.Service `json:"services"`
	Ports    []int           `json:"ports"`
	// WorktreePorts is the file's worktree_ports: how many ports a claim for
	// this project takes when it names no count (step 5A.7). Null when the
	// file has no such key.
	WorktreePorts *int `json:"worktree_ports" jsonschema:"nullable"`
}

type GroupsConfigGetParams struct {
	HostParams
	Name *string `json:"name,omitempty"`
	Path *string `json:"path,omitempty"`
}

type GroupsConfigGetResult struct {
	Path   string      `json:"path"`
	Config GroupConfig `json:"config"`
}

// GroupsConfigSetParams is one atomic edit of a `oberth.yaml` (contract §13.2,
// extended by step 5A.4). The four lists may be combined in a single call and
// are applied in this order — Remove, Rename, Add, then the Services metadata
// patches — with each list seeing the file as the previous ones left it.
// Nothing is written unless the whole edit succeeds and the result still
// validates, so a call that fails leaves the file byte-identical.
type GroupsConfigSetParams struct {
	HostParams
	Path string `json:"path"`
	// Services patches the metadata of services that already exist.
	Services []groups.ServiceEdit `json:"services,omitempty"`
	// Add appends services. A name or a port the file already uses is
	// `conflict`.
	Add []groups.ServiceAdd `json:"add,omitempty"`
	// Rename renames services everywhere in the file, depends_on references
	// included. An unknown `from` is `not_found`, a `to` already in the file is
	// `conflict`.
	Rename []groups.ServiceRename `json:"rename,omitempty"`
	// Remove deletes services and drops them from every other service's
	// depends_on. An unknown name is `not_found`.
	Remove []string `json:"remove,omitempty"`
	// WorktreePorts edits the top-level worktree_ports key (step 5A.7):
	// omitted leaves it alone, a number (1-100) writes it, and null removes
	// it. A value out of range is `invalid_config`, like any edit that would
	// leave the file invalid.
	WorktreePorts *int `json:"worktree_ports,omitempty" jsonschema:"nullable"`

	// WorktreePortsSent records that worktree_ports was present, null
	// included, because a pointer alone cannot tell null from absent.
	// UnmarshalJSON fills it; a Go caller sets it by hand.
	WorktreePortsSent bool `json:"-" jsonschema:"-"`
}

// UnmarshalJSON decodes the params and remembers whether worktree_ports was
// sent at all.
func (p *GroupsConfigSetParams) UnmarshalJSON(data []byte) error {
	type plain GroupsConfigSetParams
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	*p = GroupsConfigSetParams(v)
	_, p.WorktreePortsSent = keys[groups.FieldWorktreePorts]
	return nil
}

// WorktreePortsChange is the edit the worktree_ports field asks for.
func (p GroupsConfigSetParams) WorktreePortsChange() groups.IntChange {
	switch {
	case !p.WorktreePortsSent:
		return groups.IntChange{}
	case p.WorktreePorts == nil:
		return groups.ClearInt()
	default:
		return groups.SetInt(*p.WorktreePorts)
	}
}

// GroupsConfigSetResult is the file after the write. Affected carries the
// service names the edit touched, in the order it applied them — a removed
// name, a rename's new name, an added name, a patched name: this method mutates
// a config, not a port, so there is no port key to report (step 1A.7).
type GroupsConfigSetResult struct {
	MutationResult
	Path   string      `json:"path"`
	Config GroupConfig `json:"config"`
}

type GroupsReloadResult struct {
	Loaded int             `json:"loaded"`
	Errors []ConfigProblem `json:"errors"`
}

// ConfigProblem is one `oberth.yaml` that could not be used.
type ConfigProblem struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// GroupsInitParams asks for a proposed `oberth.yaml` for the checkout at
// RootDir. Write is the contract's opt-in to actually writing it, so the
// default is a preview; Force overwrites an existing file, while Merge appends
// into one instead (contract §4, §16, step 5A.4). Force and Merge are mutually
// exclusive.
type GroupsInitParams struct {
	HostParams
	RootDir string `json:"root_dir"`
	Write   bool   `json:"write,omitempty"`
	Force   bool   `json:"force,omitempty"`
	Merge   bool   `json:"merge,omitempty"`
	// Services replaces the proposed service list with the caller's own, so a
	// curated file can be written in one call. The entries are taken as given:
	// nothing is copied onto them from what happens to be listening on their
	// ports.
	Services []groups.ServiceAdd `json:"services,omitempty"`
	// YAML is the whole file, when the caller edited the proposal itself. The
	// app's preview pane is editable, and what lands on disk has to be what the
	// user approved rather than a re-render of it — the moment option-berth
	// re-renders an edit, the edit is not the user's any more.
	//
	// It goes through the same parse a generated file does, so a broken edit is
	// refused with the line that broke it and nothing is written.
	YAML string `json:"yaml,omitempty"`
}

type GroupsInitResult struct {
	MutationResult
	Path     string      `json:"path"`
	YAML     string      `json:"yaml"`
	Proposal state.Group `json:"proposal"`
}

// ------------------------------------------------------------------ runs ---

type RunsRegisterParams struct {
	HostParams
	PID       int     `json:"pid"`
	PPID      int     `json:"ppid"`
	Group     string  `json:"group"`
	Name      string  `json:"name"`
	Cmd       string  `json:"cmd"`
	Cwd       string  `json:"cwd"`
	PortHint  *int    `json:"port_hint,omitempty"`
	StartedAt string  `json:"started_at"`
	ID        *string `json:"id,omitempty"`
	// Session is the agent session that asked for this run (spec 2 §3). The
	// caller detects it: `oberth start` reads its own environment, which is the
	// agent's, while the daemon's is not.
	Session *state.Session `json:"session,omitempty"`
	// AllowOutsideHome opts out of the daemon's refusal to record a run whose
	// cwd is outside the user's home (daemon spec, "Transport details"). The
	// CLI sets it; the MCP server does not.
	AllowOutsideHome bool `json:"allow_outside_home,omitempty"`
}

type RunsRegisterResult struct {
	ID string `json:"id"`
}

type RunsUnregisterParams struct {
	HostParams
	PID int `json:"pid"`
	// ExitCode is how the run ended, for a caller that waited on it —
	// `oberth start` without --detach. Absent simply forgets the run.
	ExitCode *int `json:"exit_code,omitempty"`
	// Stopped says the run was asked to stop (a Ctrl+C), so a non-zero exit
	// code is not a crash.
	Stopped bool `json:"stopped,omitempty"`
}

type RunsListResult struct {
	Runs []RunRecord `json:"runs"`
	// Exited is the runs that have ended, newest first, with their exit code
	// and the last lines they logged. The daemon keeps a bounded history in its
	// store and restores it when it restarts.
	Exited []RunRecord `json:"exited"`
}

type RunRecord struct {
	ID        string `json:"id"`
	PID       int    `json:"pid"`
	Group     string `json:"group"`
	Name      string `json:"name"`
	Cmd       string `json:"cmd"`
	Cwd       string `json:"cwd"`
	StartedAt string `json:"started_at"`
	Ports     []int  `json:"ports"`
	// PortHint is the port `oberth start --port` said this run would bind, or
	// the one groups.start assigned a `port: auto` service.
	PortHint *int `json:"port_hint,omitempty"`
	// URL is where that port answers, for a run that has one.
	URL string `json:"url,omitempty"`
	// Status is "starting" while a run with a port hint has not bound it yet,
	// "running" otherwise, and "exited" for a run in the exited list.
	Status string `json:"status"`
	// ConfigPath, StartID and Origin say where the run came from: the
	// oberth.yaml it was started from, the `groups.start` that started it with
	// its siblings, and the client that asked (cli, app, mcp).
	ConfigPath string `json:"config_path,omitempty"`
	StartID    string `json:"start_id,omitempty"`
	Origin     string `json:"origin,omitempty"`
	// SpecHash identifies the executable manifest fields used for this run;
	// environment values are intentionally excluded.
	SpecHash string `json:"spec_hash,omitempty"`
	// LogPath is the file a detached run's output goes to.
	LogPath string `json:"log_path,omitempty"`
	// ExitCode, Reason, ExitedAt and LastLines are filled in for a run that
	// has ended. Reason is exited (code 0), crashed (any other code),
	// port_occupied (a declared port was already held), stopped
	// (option-berth or the user asked it to stop), or ready_timeout.
	ExitCode  *int     `json:"exit_code,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	ExitedAt  string   `json:"exited_at,omitempty"`
	LastLines []string `json:"last_lines,omitempty"`
}

type RunsSpawnParams struct {
	HostParams
	Argv             []string          `json:"argv"`
	Cwd              string            `json:"cwd"`
	Env              map[string]string `json:"env,omitempty"`
	Group            *string           `json:"group,omitempty"`
	Name             *string           `json:"name,omitempty"`
	PortHint         *int              `json:"port_hint,omitempty"`
	Session          *state.Session    `json:"session,omitempty"`
	AllowOutsideHome bool              `json:"allow_outside_home,omitempty"`
}

type RunsSpawnResult struct {
	MutationResult
	RunID   string `json:"run_id"`
	PID     int    `json:"pid"`
	LogPath string `json:"log_path"`
}

type SessionsKillParams struct {
	HostParams
	ID     string `json:"id"`
	Tree   bool   `json:"tree,omitempty"`
	Force  bool   `json:"force,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// ---------------------------------------------------------------- config ---

type ConfigGetResult struct {
	Config map[string]any `json:"config"`
}

type ConfigSetParams struct {
	HostParams
	Patch map[string]any `json:"patch"`
}

type ConfigSetResult struct {
	OK     bool           `json:"ok"`
	Config map[string]any `json:"config"`
}

type ConfigPathResult struct {
	Path string `json:"path"`
}
