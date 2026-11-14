package rpc

import (
	"encoding/json"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// This file declares what a client sends and reads back for every method in the
// table: one params type and one result type per method, plus the shared pieces
// they are built from. The published schema is reflected from these types, so a
// field added here is a promise to every client; docs/cli.md is where the same
// promise is written down for readers, and the two move in one change.

// ---------------------------------------------------------------- shared ---

// Selector names the one port a call acts on. A caller sets exactly one of
// Port, PID and RunID; BindAddress only tells two listeners on the same port
// apart.
//
// A client holding a port key from the stream takes it apart itself and sends
// Port with BindAddress. The daemon reads the fields it acts on and nothing
// else: a joined spelling it would have to expand first is the shape that gets
// accepted and ignored, so no such field is published.
type Selector struct {
	Port        *int    `json:"port,omitempty"`
	PID         *int    `json:"pid,omitempty"`
	BindAddress *string `json:"bind_address,omitempty"`
	RunID       *string `json:"run_id,omitempty"`
}

// MutationResult is what every mutating method returns at minimum. Affected
// lists the ports the call touched, each as "<port>:<bind_address>"; a method
// that edits a manifest reports the service names it changed there instead.
type MutationResult struct {
	OK       bool     `json:"ok"`
	Affected []string `json:"affected"`
}

// KillEnvelope wraps the per-target outcome of a kill-shaped method. The rows
// are state.KillResult — one Go type the killer, the CLI and the daemon all
// fill in — so the schema publishes it under its plain name and this envelope
// stays named for what it wraps.
type KillEnvelope struct {
	MutationResult
	Results []state.KillResult `json:"results"`
	// Released counts the claimed ports a `groups.kill` with release handed
	// back, which is the `oberth down` path. Every other kill leaves it zero.
	Released int `json:"released,omitempty"`
}

// Empty stands in for the params or result of a method with nothing to carry.
type Empty struct{}

// OKResult is the whole of a reply that only says the call went through.
type OKResult struct {
	OK bool `json:"ok"`
}

// Capabilities are the optional behaviours a daemon announces in
// daemon.hello. A client checks for one before sending a manifest or a field an
// older daemon would drop without saying so: that silent drop, not the older
// daemon itself, is what every name here exists to prevent.
const (
	// CapabilityAutoPorts covers `port: auto`, `env:` and ${…} references. An
	// older daemon parses such a file itself and reports it invalid, which
	// blames the file for the daemon's age.
	CapabilityAutoPorts = "groups.autoports"

	// CapabilityManifestExtras covers the manifest's `env_file:` and
	// `workspace:` references. An older daemon ignores both, so a service would
	// start without the env file's values and a workspace address would stay
	// unexpanded.
	CapabilityManifestExtras = "groups.manifest.extras"

	// CapabilityStartDependencies covers `include_dependencies: true` in
	// groups.start: growing the named services' plan with their transitive
	// depends_on closure. An older daemon starts only the named services.
	CapabilityStartDependencies = "groups.start.dependencies"

	// CapabilityReadyTimeout covers the manifest's per-service `ready_timeout:`
	// — both the dependency waits and the budget a start chunk publishes. An
	// older daemon keeps its fixed 30s and would fail a slow dependency at the
	// wrong deadline.
	CapabilityReadyTimeout = "groups.start.ready_timeout"

	// CapabilityStateScope covers scoped state snapshots and the redacted
	// state.changed notification.
	CapabilityStateScope = "state.scope"

	// CapabilityStartPhases covers a service's in-flight start attempt:
	// progress chunks on `groups.start` when the caller asks for them, and a
	// `starting` object on the service row. It is diagnostic, never a
	// prerequisite — an old client that does not set progress keeps the old
	// stream, and a new client talking to an old daemon simply gets no
	// progress.
	CapabilityStartPhases = "groups.start.phases"

	// CapabilityKillExpectRuns covers `expect_runs` in groups.kill: a stop
	// scoped to the runs the caller started, so a service another controller
	// restarted in the meantime is left alone. An older daemon ignores the
	// field and stops whatever answers to the service's name, which is exactly
	// the replacement run that field exists to protect.
	CapabilityKillExpectRuns = "groups.kill.expect_runs"
)

// Include lists the optional per-subscriber enrichments ("stats", "health").
type Include []string

// ---------------------------------------------------------------- daemon ---

type DaemonHelloParams struct {
	Client        string `json:"client"` // the engine's own client sends "cli"
	ClientVersion string `json:"client_version"`
	Keepalive     bool   `json:"keepalive,omitempty"`
}

// ObservationStatus is the process's self-probe verdict or the collector's
// partial-scan status. It lives in state so a published snapshot can carry it;
// the alias keeps existing callers on the same wire shape.
type ObservationStatus = state.Observation

type DaemonHelloResult struct {
	ProtocolVersion string             `json:"protocol_version"`
	DaemonVersion   string             `json:"daemon_version"`
	PID             int                `json:"pid"`
	StartedAt       string             `json:"started_at"`
	Capabilities    []string           `json:"capabilities"`
	Socket          string             `json:"socket"`
	BinaryPath      string             `json:"binary_path"`
	Keepalive       bool               `json:"keepalive"`
	Observation     *ObservationStatus `json:"observation,omitempty"`
}

type DaemonStatusResult struct {
	// PID and Uptime identify the process and say how long it has been
	// answering; Subscribers counts the live state subscriptions, and LastScanAt
	// is when the port scanner last finished a pass.
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
	// ScanIntervalMs is where the adaptive port-scan cadence sits right now: it
	// moves between the base and a ceiling as scans come back unchanged.
	ScanIntervalMs int `json:"scan_interval_ms"`
	// ScanBaseIntervalMs and StatsIntervalMs are the settings behind that
	// cadence: `daemon.scan_interval` and `daemon.stats_interval` as this daemon
	// resolved them at startup. Both are read once, so checking them is how you
	// tell whether a config edit took without restarting the daemon to guess.
	ScanBaseIntervalMs int `json:"scan_base_interval_ms"`
	StatsIntervalMs    int `json:"stats_interval_ms"`
	// Scans counts the port scans this daemon has run. Two clients reading
	// through the daemon must not make it grow faster than one does.
	Scans  int64  `json:"scans"`
	DBPath string `json:"db_path"`
	// Observation says whether this daemon can see its own listeners. A daemon
	// started inside a sandbox can run while blind; the CLI and the desktop app
	// need to see that before trusting an empty port list.
	Observation *ObservationStatus `json:"observation,omitempty"`
}

// DaemonSchemaResult is the JSON Schema bundle this package generates.
type DaemonSchemaResult struct {
	Schema json.RawMessage `json:"schema"`
}

// DoctorCheck is one diagnostic `oberth doctor` and `daemon.doctor` report. It
// is the single Go type behind the table, the CLI's `--json` and the wire
// result, so a client that renders one renders the other.
type DoctorCheck struct {
	// ID names the check. A check that exists once per external tool is dotted
	// with that tool: `mcp_registered.claude_code`.
	ID string `json:"id"`
	// Status is the verdict: ok, warn, fail or skip. Only `fail` makes the whole
	// run not ok.
	Status string `json:"status"`
	// Summary is the one line a table row shows.
	Summary string `json:"summary"`
	// Detail is the evidence: paths, versions, the parse error. It is also where
	// a check the daemon cannot run from its own process says so.
	Detail string `json:"detail,omitempty"`
	// Fix is the human hint: what to run, or what to edit.
	Fix string `json:"fix,omitempty"`
	// Fixable says `oberth doctor --fix` can repair this one unattended.
	Fixable bool `json:"fixable"`
}

type DaemonDoctorParams struct {
	// Only keeps just these checks. An entry matches a check id exactly, or
	// every check under it when the id names a tool and a single check with a
	// dot (`mcp_registered`).
	Only []string `json:"only,omitempty"`
	// Project is the directory the project-scoped checks look at. Empty means
	// the process's own working directory — for the daemon, wherever it was
	// started — so a client that cares sends it.
	Project string `json:"project,omitempty"`
}

// DaemonDoctorResult is what both `oberth doctor --json` and `daemon.doctor`
// return. OK is false when at least one check failed.
type DaemonDoctorResult struct {
	OK     bool          `json:"ok"`
	Checks []DoctorCheck `json:"checks"`
	// Version is the version of the process that ran the checks: the CLI for
	// `oberth doctor`, the daemon for `daemon.doctor`.
	Version string `json:"version"`
	// DaemonVersion is the version the daemon answered with, empty when no
	// daemon is reachable: `oberth doctor` still runs with none up.
	DaemonVersion string `json:"daemon_version"`
}

// ----------------------------------------------------------------- state ---

type StateSnapshotParams struct {
	Include Include `json:"include,omitempty"`
	// Scope is sent as filter. Nil keeps the machine-wide snapshot a client
	// written before scopes existed expects; a scoped request returns the
	// redacted stream projection instead of command and config details.
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
	// AfterSeq asks the daemon to resume from this published sequence. When the
	// in-memory replay window no longer covers it, subscribe safely falls back
	// to a fresh snapshot.
	AfterSeq uint64 `json:"after_seq,omitempty"`
	// Scope is sent as filter. Nil keeps the machine-wide subscription clients
	// written before scopes existed rely on; a scoped subscription reads the
	// redacted state.changed stream and must set Events.
	Scope *state.Scope `json:"filter,omitempty"`
}

// ----------------------------------------------------------------- ports ---

type PortsKillParams struct {
	Targets []Selector `json:"targets"`
	// Tree stops each target's whole process tree. A listener an
	// option-berth-started run owns is stopped as a tree either way.
	Tree bool `json:"tree,omitempty"`
	// Force is already the strongest signal, so it skips the wait; GraceMs is
	// the SIGTERM to SIGKILL window, zero meaning the engine's default.
	Force   bool `json:"force,omitempty"`
	GraceMs int  `json:"grace_ms,omitempty"`
	// Escalate is three-valued: nil escalates after the grace window, an
	// explicit false stops at SIGTERM. DryRun plans the kill and performs none
	// of it.
	Escalate *bool `json:"escalate,omitempty"`
	DryRun   bool  `json:"dry_run,omitempty"`
}

type PortsLogsParams struct {
	Selector
	Lines  int  `json:"lines,omitempty"`
	Follow bool `json:"follow,omitempty"`
}

// PortsLogsResult answers a one-shot call. With follow set, the reply also
// carries a subscription_id and the lines keep arriving as PortsLogsChunk.
type PortsLogsResult struct {
	Source string   `json:"source"`
	Lines  []string `json:"lines"`
	// Truncated says the tail filled the requested line count, so Lines is a
	// window onto the end of the log rather than the whole of it.
	Truncated bool `json:"truncated"`
	// SubscriptionID is set only on a follow, which keeps sending the rest of
	// the output as PortsLogsChunk.
	SubscriptionID string `json:"subscription_id,omitempty"`
}

// PortsLogsChunk is one line pushed by a following ports.logs. Source is
// repeated on every chunk so a multiplexed reader can tell two logs apart.
type PortsLogsChunk struct {
	Source string `json:"source"`
	Line   string `json:"line"`
}

// ---------------------------------------------------------------- groups ---

type GroupsListResult struct {
	Groups []state.Group `json:"groups"`
}

// GroupsCodeParams names one group. The comparison only runs for its running
// services, so a reader must say which group it is asking about.
type GroupsCodeParams struct {
	Name string `json:"name"`
}

// GroupsCodeResult carries the same nullable code-identity verdicts `status`
// publishes. A service absent from Services has no answer (unknown), and null
// fields inside a row mean unknown; neither is "unchanged".
type GroupsCodeResult struct {
	Services []ServiceCode `json:"services"`
}

// ServiceCode is one running service's start-time code identity compared with
// what is on disk now.
type ServiceCode struct {
	Name  string `json:"name"`
	RunID string `json:"run_id"`
	// CodeHead is the commit checked out when this run started, when the
	// checkout had one.
	CodeHead *string `json:"code_head,omitempty" jsonschema:"nullable"`
	// Changed is true/false when the comparison ran, null when unknown.
	Changed *bool `json:"code_changed_since_start,omitempty" jsonschema:"nullable"`
	// ChangedFiles is the exact number of differing files, when both
	// snapshots kept per-file evidence.
	ChangedFiles *int    `json:"code_changed_files,omitempty" jsonschema:"nullable"`
	Hint         *string `json:"code_hint,omitempty" jsonschema:"nullable"`
}

type GroupsKillParams struct {
	Name string `json:"name"`
	// ConfigPath names the group by its oberth.yaml instead of by name, for a
	// caller that knows the file but not the name the daemon publishes its
	// group under (`<project>@<worktree>` in a linked worktree, an alias).
	ConfigPath *string `json:"config_path,omitempty"`
	// Worktree names the checkout by its root path. It is the explicit stop
	// entry for an orchestrator after the directory was deleted or moved: the
	// daemon resolves the recorded group from the identity ledger instead of
	// reading a manifest that may no longer exist. Mutually exclusive with
	// Name, ConfigPath and Identity.
	WorktreePath *string `json:"worktree_path,omitempty"`
	// Identity names the checkout by its `${worktree}` token (`wt_...`).
	// Like Worktree, it reaches services whose directory is gone and releases
	// the reservations recorded for that checkout.
	Identity *string `json:"identity,omitempty"`
	Force    bool    `json:"force,omitempty"`
	GraceMs  int     `json:"grace_ms,omitempty"`
	DryRun   bool    `json:"dry_run,omitempty"`
	// Only narrows a project stop to these declared services. It is used by
	// `oberth restart --only`; scoped stops keep auto-port claims.
	Only []string `json:"only,omitempty"`
	// ExpectRuns names the run each service must still be for this stop to
	// touch it. It is the cleanup a readiness timeout uses: it stops the runs
	// the caller started, not whatever now answers to the service's name, so a
	// run another controller started during the wait is left alone. Mutually
	// exclusive with Only — Only selects by service and would widen the stop
	// past the named runs. Also scoped: a stop with expectations keeps the
	// group's `port: auto` claims.
	ExpectRuns []RunExpectation `json:"expect_runs,omitempty"`
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

// RunExpectation is one service's expected run identity in a stop. Both fields
// are required: without the run id the stop would be a name-only stop, which is
// the thing this selector exists to avoid.
type RunExpectation struct {
	// Service is a service the group's manifest declares.
	Service string `json:"service"`
	// RunID is the run this caller started and may stop. A live run with the
	// same service name but another id is not a target.
	RunID string `json:"run_id"`
}

type GroupsStartParams struct {
	Name             *string  `json:"name,omitempty"`
	ConfigPath       *string  `json:"config_path,omitempty"`
	Only             []string `json:"only,omitempty"`
	AllowOutsideHome bool     `json:"allow_outside_home,omitempty"`
	// IncludeDependencies asks for the transitive depends_on closure of Only,
	// not only the named services. `oberth up` sets it; `oberth restart` leaves
	// it false and keeps restarting exactly what it names. Unknown names in
	// Only are still not_found with or without it.
	IncludeDependencies bool `json:"include_dependencies,omitempty"`
	// Env is what the services start in, layered over the daemon's own
	// environment. The CLI sends its shell's, so a service sees the PATH,
	// toolchain and virtualenv it was started from rather than the daemon's.
	// Omitted, the services inherit the daemon's.
	Env map[string]string `json:"env,omitempty"`
	// Progress asks the daemon to push in-flight `starting` chunks while a
	// service waits for a dependency, runs prepare, or is being spawned. They
	// are diagnostic, not outcomes: the per-service outcome chunks and
	// GroupsStartEnd are unchanged. Omitted (the historical shape) means no
	// progress chunks at all.
	Progress bool `json:"progress,omitempty"`
}

type GroupsStartResult struct {
	MutationResult
	SubscriptionID string `json:"subscription_id"`
	// StartID is shared by every run this call starts, so the services brought
	// up together can be told apart from the ones already running.
	StartID string `json:"start_id,omitempty"`
}

// GroupsStartChunk is one service's outcome, pushed as it happens: it was
// started (pid and log_path), it was skipped (with the reason), or it could not
// be started (error). When the caller set progress, it can also be an in-flight
// `starting` observation with a phase and timings; those are not outcomes.
type GroupsStartChunk struct {
	Service string `json:"service"`
	// State is the machine-readable outcome for this service: started,
	// skipped, failed, or ready_timeout (when the CLI was asked to wait).
	// `starting` is an in-flight progress observation requested with
	// GroupsStartParams.Progress; it never appears in the final summary.
	State string `json:"state,omitempty" jsonschema:"enum=started,enum=skipped,enum=failed,enum=ready_timeout,enum=starting"`
	// Ready is the CLI's readiness annotation, not a daemon fact: true only
	// when this invocation waited and confirmed the service. false on a
	// started service means "started, readiness not confirmed". Absent for
	// skipped and failed services.
	Ready *bool `json:"ready,omitempty"`
	PID   int   `json:"pid,omitempty"`
	// Port is the port a started service was told to bind: its fixed port,
	// or the one assigned for `port: auto`. Zero for a service with none.
	Port    int    `json:"port,omitempty"`
	LogPath string `json:"log_path,omitempty"`
	// RunID is the run this chunk is about: the run a started service became,
	// or the live run a skipped chunk matched. It is empty when the daemon
	// cannot name one (an unmanaged listener, or an older daemon).
	RunID string `json:"run_id,omitempty"`
	// ReadyTimeout is this service's effective readiness budget, a normalized
	// Go duration ("30s"). A daemon that predates the field omits it, and a
	// client falls back to 30s.
	ReadyTimeout string `json:"ready_timeout,omitempty"`
	// The remaining fields are present only on a `starting` progress chunk.
	// They describe the in-flight start attempt, never a committed outcome.
	// Phase is waiting_dependency while depends_on is waited for, prepare while
	// the prepare command runs, and spawning from prepare success until the run
	// is registered. StartedAt is when the attempt began (RFC3339Nano);
	// ElapsedMS is computed at send time; WaitingOn names the declared
	// dependencies this start waits for; LastPrepareMS is the previous prepare
	// duration when one is known.
	Phase         string   `json:"phase,omitempty" jsonschema:"enum=waiting_dependency,enum=prepare,enum=spawning"`
	StartedAt     string   `json:"started_at,omitempty"`
	ElapsedMS     int64    `json:"elapsed_ms,omitempty"`
	WaitingOn     []string `json:"waiting_on,omitempty"`
	LastPrepareMS *int64   `json:"last_prepare_ms,omitempty" jsonschema:"nullable"`
	// LogOffset is how long log_path already was when this service started. The
	// file is appended to across runs, so a client that follows it begins here,
	// which shows this run and not the generations before it.
	LogOffset int64  `json:"log_offset,omitempty"`
	Skipped   bool   `json:"skipped,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
	// Hint is a deterministic next command for a failed service, or for a
	// skipped service whose listener belongs to a process the start did not
	// create. It contains no model judgement; it points at evidence
	// option-berth already collected.
	Hint string `json:"hint,omitempty"`
	// PortHolder names the process observed on the declared port when this
	// service failed with port_occupied. Absent for every other outcome.
	PortHolder *state.PortHolder `json:"port_holder,omitempty"`
	// IncludedBy lists the explicitly named services whose depends_on closure
	// brought this service into the plan, sorted. Empty means the service was
	// named (or every service was planned), so nothing included it implicitly.
	IncludedBy []string `json:"included_by,omitempty"`
}

type GroupsStartEnd struct {
	Started []string `json:"started"`
	Skipped []string `json:"skipped"`
	Errors  []string `json:"errors"`
	// Included lists, sorted, the services in the plan only because a named
	// service depends on them. It is a plan fact, independent of whether each
	// one started or was already running; empty when nothing was included.
	Included []string `json:"included,omitempty"`
}

// GroupsRenameParams renames a project. Name is the project's name
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
	Name string `json:"name"`
	To   string `json:"to"`
}

// GroupsRenameResult reports what the rename changed: Affected holds the new
// name of every group it touched — the project's and each checkout's, sorted —
// and Name the project's new name. When the project already had that name,
// Affected is empty and Name still carries the unchanged name.
type GroupsRenameResult struct {
	MutationResult
	Name string `json:"name"`
}

// GroupConfig is a `oberth.yaml` as the protocol carries it: the group name,
// the services as contract rows, and the extra ports the file claims. It is
// the `config` of groups.config.get and groups.config.set.
type GroupConfig struct {
	Name     string          `json:"name"`
	Services []state.Service `json:"services"`
	Ports    []int           `json:"ports"`
	// WorktreePorts is the file's worktree_ports: the number of ports a claim for
	// this project takes when it names no count. Null when the key is absent.
	WorktreePorts *int `json:"worktree_ports" jsonschema:"nullable"`
}

type GroupsConfigGetParams struct {
	// Name and Path name the file to read: the group as the daemon publishes it,
	// or the oberth.yaml itself. A path wins when both are sent.
	Name *string `json:"name,omitempty"`
	Path *string `json:"path,omitempty"`
}

// GroupsConfigGetResult is where the file was found and what it declares.
type GroupsConfigGetResult struct {
	Path   string      `json:"path"`
	Config GroupConfig `json:"config"`
}

// GroupsConfigSetParams is one atomic edit of an `oberth.yaml`. The four lists
// may be combined in a single call, and they are applied in this order —
// Remove, Rename, Add, then the Services metadata patches — each list seeing the
// file as the previous ones left it. Nothing is written unless the whole edit
// succeeds and the result still validates, so a failed call leaves the file
// byte-identical.
type GroupsConfigSetParams struct {
	Path string `json:"path"`
	// Services patches the metadata of services that already exist; Add appends
	// new ones, where a name or a port the file already uses is `conflict`.
	Services []groups.ServiceEdit `json:"services,omitempty"`
	Add      []groups.ServiceAdd  `json:"add,omitempty"`
	// Rename renames services everywhere in the file, depends_on references
	// included. An unknown `from` is `not_found`; a `to` already in the file is
	// `conflict`.
	Rename []groups.ServiceRename `json:"rename,omitempty"`
	// Remove deletes services, and drops them from every other service's
	// depends_on. An unknown name is `not_found`.
	Remove []string `json:"remove,omitempty"`
	// WorktreePorts edits the top-level worktree_ports key: omitted leaves it
	// alone, a number in 1-100 writes it, and null removes it. A value out of
	// range is `invalid_config`, like any edit that would leave the file
	// invalid.
	WorktreePorts *int `json:"worktree_ports,omitempty" jsonschema:"nullable"`

	// WorktreePortsSent records that worktree_ports was present, null included:
	// a pointer alone cannot tell null from absent. UnmarshalJSON fills it in; a
	// Go caller sets it by hand.
	WorktreePortsSent bool `json:"-" jsonschema:"-"`
}

// UnmarshalJSON decodes the params and remembers whether worktree_ports was sent
// at all.
func (p *GroupsConfigSetParams) UnmarshalJSON(data []byte) error {
	// The alias drops this type's own UnmarshalJSON, so decoding it cannot
	// recurse back here.
	type wire GroupsConfigSetParams
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	// Only the key set distinguishes a null from an absent field: both land as
	// a nil pointer in decoded, and only a null leaves bytes behind here.
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(data, &sent); err != nil {
		return err
	}
	*p = GroupsConfigSetParams(decoded)
	_, p.WorktreePortsSent = sent[groups.FieldWorktreePorts]
	return nil
}

// WorktreePortsChange is the edit the worktree_ports field asks for.
func (p GroupsConfigSetParams) WorktreePortsChange() groups.IntChange {
	if !p.WorktreePortsSent {
		return groups.IntChange{}
	}
	if p.WorktreePorts == nil {
		return groups.ClearInt()
	}
	return groups.SetInt(*p.WorktreePorts)
}

// GroupsConfigSetResult is the file after the write. Affected carries the
// service names the edit touched, in the order it applied them — a removed
// name, a rename's new name, an added name, a patched name: this method mutates
// a config, not a port, so there is no port key to report.
type GroupsConfigSetResult struct {
	MutationResult
	Path   string      `json:"path"`
	Config GroupConfig `json:"config"`
}

// GroupsReloadResult reports a reload of every known manifest: how many files
// loaded, and the ones that could not be used.
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
// RootDir.
type GroupsInitParams struct {
	RootDir string `json:"root_dir"`
	// Write is the contract's opt-in to actually writing the file, so the
	// default is a preview.
	Write bool `json:"write,omitempty"`
	// Force overwrites an existing file and Merge appends into one instead; the
	// two are mutually exclusive.
	Force bool `json:"force,omitempty"`
	Merge bool `json:"merge,omitempty"`
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

// GroupsInitResult is the file as it now stands, together with the proposal it
// was written from.
type GroupsInitResult struct {
	MutationResult
	// Path is where the file now is, YAML what it holds, and Proposal the parsed
	// rows a client renders its preview from.
	Path     string      `json:"path"`
	YAML     string      `json:"yaml"`
	Proposal state.Group `json:"proposal"`
}

// ------------------------------------------------------------------ runs ---

// RunsRegisterParams is what a caller that started a process itself reports:
// which process it is, what it was started as, and where.
type RunsRegisterParams struct {
	// PID is the process the caller started and PPID its parent; Group and Name
	// are the manifest identity it was started under.
	PID   int    `json:"pid"`
	PPID  int    `json:"ppid"`
	Group string `json:"group"`
	Name  string `json:"name"`
	// Cmd and Cwd are what it was started with, and StartedAt when it began;
	// PortHint is the port it is expected to bind, if the caller knows one.
	Cmd       string `json:"cmd"`
	Cwd       string `json:"cwd"`
	PortHint  *int   `json:"port_hint,omitempty"`
	StartedAt string `json:"started_at"`
	// ID lets the caller name the run itself. Absent, the registry gives it one,
	// which is what keeps a recycled pid attributable.
	ID *string `json:"id,omitempty"`
	// Session is the agent session that asked for this run. The caller detects
	// it: `oberth start` reads its own environment, which is the agent's, while
	// the daemon's is not.
	Session *state.Session `json:"session,omitempty"`
	// AllowOutsideHome opts out of the daemon's refusal to record a run whose
	// cwd is outside the user's home. `oberth start` sets it unconditionally:
	// that command runs as the user and has no `--allow-outside-home` flag, so
	// the refusal here protects callers that do not already speak for the user.
	AllowOutsideHome bool `json:"allow_outside_home,omitempty"`
}

type RunsRegisterResult struct {
	ID string `json:"id"`
}

type RunsUnregisterParams struct {
	PID int `json:"pid"`
	// ExitCode is how the run ended, for a caller that waited on it —
	// `oberth start` without --detach. Absent simply forgets the run.
	ExitCode *int `json:"exit_code,omitempty"`
	// Stopped says the run was asked to stop (a Ctrl+C, say), so a non-zero exit
	// code is not a crash.
	Stopped bool `json:"stopped,omitempty"`
}

type RunsListResult struct {
	Runs []RunRecord `json:"runs"`
	// Exited is the runs that have ended, newest first, with the code each one
	// exited on and the last lines it logged. The daemon keeps a bounded history
	// in its store and restores it when it restarts.
	Exited []RunRecord `json:"exited"`
}

type RunRecord struct {
	ID  string `json:"id"`
	PID int    `json:"pid"`
	// Group and Name are the manifest identity the run was started under; Cmd
	// and Cwd are what it was started with.
	Group string `json:"group"`
	Name  string `json:"name"`
	Cmd   string `json:"cmd"`
	Cwd   string `json:"cwd"`
	// StartedAt is RFC 3339, and Ports are the ones the last scan saw this run
	// holding.
	StartedAt string `json:"started_at"`
	Ports     []int  `json:"ports"`
	// PortHint is the port `oberth start --port` said this run would bind, or
	// the one groups.start assigned a `port: auto` service.
	PortHint *int `json:"port_hint,omitempty"`
	// URL is the address that port answers on, for a run that has one.
	URL string `json:"url,omitempty"`
	// Status is "starting" while a port-hinted run has not bound its port yet,
	// "running" otherwise, and "exited" for a run that is in the exited list.
	Status string `json:"status"`
	// ConfigPath, StartID and Origin say where the run came from: the
	// oberth.yaml it was started from, the `groups.start` that started it with
	// its siblings, and the client name reported in daemon.hello.
	ConfigPath string `json:"config_path,omitempty"`
	StartID    string `json:"start_id,omitempty"`
	Origin     string `json:"origin,omitempty"`
	// SpecHash identifies the executable manifest fields used for this run;
	// environment values are intentionally excluded.
	SpecHash string `json:"spec_hash,omitempty"`
	// LogPath is the file a detached run's output goes to.
	LogPath string `json:"log_path,omitempty"`
	// LogOffset is where this run's part of LogPath begins. The file is
	// appended to across runs, so a reader starts here to show one run and not
	// the generations before it. It is null when an older daemon did not record
	// the offset: zero is a real value (the file was empty at start), so the
	// two must not share a spelling.
	LogOffset *int64 `json:"log_offset,omitempty"`
	// ExitCode, Reason, ExitedAt and LastLines are filled in for a run that
	// has ended. Reason is exited (code 0), crashed (any other code),
	// port_occupied (a declared port was already held), stopped
	// (option-berth or the user asked it to stop), or ready_timeout.
	ExitCode  *int     `json:"exit_code,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	ExitedAt  string   `json:"exited_at,omitempty"`
	LastLines []string `json:"last_lines,omitempty"`
	// Hint is a deterministic next command for a failed run, pointing at
	// evidence the run actually left (its log file). Same field name and
	// meaning as GroupsStartChunk.Hint; empty for routine exits and for
	// failures that never created a log. Status fills it while rendering
	// exits; a runs.list that does not compute one leaves it empty.
	Hint string `json:"hint,omitempty"`
	// PortHolder is the process observed on the declared port when a run
	// ended with port_occupied. Absent for every other outcome.
	PortHolder *state.PortHolder `json:"port_holder,omitempty"`
}

type RunsSpawnParams struct {
	Argv []string `json:"argv"`
	// Cwd is where the command runs. Omitted, the daemon resolves its own
	// working directory.
	Cwd string `json:"cwd"`
	// Env is layered over the daemon's own environment.
	Env map[string]string `json:"env,omitempty"`
	// Group and Name are the identity the caller wants the run recorded under;
	// they are resolved, not taken literally.
	Group *string `json:"group,omitempty"`
	Name  *string `json:"name,omitempty"`
	// PortHint is the port the caller expects this run to bind.
	PortHint *int `json:"port_hint,omitempty"`
	// Session is the agent session that asked for the run, and AllowOutsideHome
	// opts out of the daemon's home-directory refusal.
	Session          *state.Session `json:"session,omitempty"`
	AllowOutsideHome bool           `json:"allow_outside_home,omitempty"`
}

// RunsSpawnResult is the started run: its id, the process behind it, and the
// file its output goes to.
type RunsSpawnResult struct {
	MutationResult
	RunID string `json:"run_id"`
	PID   int    `json:"pid"`
	// LogPath is where the detached process appends what it prints.
	LogPath string `json:"log_path"`
}

// SessionsKillParams stops what a session started. ID names the session; Tree,
// Force and DryRun mean what they mean for a port kill.
type SessionsKillParams struct {
	ID   string `json:"id"`
	Tree bool   `json:"tree,omitempty"`
	// Force skips the polite signal, and DryRun only reports what would be
	// stopped.
	Force  bool `json:"force,omitempty"`
	DryRun bool `json:"dry_run,omitempty"`
}

// ---------------------------------------------------------------- config ---

// ConfigGetResult is the daemon's config file as a plain map, so a key this
// build does not know survives a round trip through it.
type ConfigGetResult struct {
	Config map[string]any `json:"config"`
}

// ConfigSetParams carries the keys to change. They are merged into the file as
// written, which is why the same map type comes back.
type ConfigSetParams struct {
	Patch map[string]any `json:"patch"`
}

// ConfigSetResult is the config as it stands after the write.
type ConfigSetResult struct {
	OK     bool           `json:"ok"`
	Config map[string]any `json:"config"`
}

// ConfigPathResult is where the daemon reads and writes its config.
type ConfigPathResult struct {
	Path string `json:"path"`
}
