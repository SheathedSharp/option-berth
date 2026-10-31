package state

// Service is one entry of a group's `oberth.yaml` services list. PortActual is
// the port the service is actually listening on right now, resolved by the
// group resolver (spec 3 needs this join).
type Service struct {
	Name    string `json:"name"`
	Prepare string `json:"prepare"`
	// PrepareInputs are the files the prepare step reads, relative to Cwd. When
	// their contents are unchanged, `up` skips the step. Always an array.
	PrepareInputs []string `json:"prepare_inputs"`
	Cmd           string   `json:"cmd"`
	Cwd           string   `json:"cwd"`
	// Sources is the manifest's `sources:` list, relative to Cwd. Null means
	// the service directory (the default); an empty array means the code check
	// is explicitly disabled.
	Sources []string `json:"sources" jsonschema:"nullable"`
	// ReadyTimeout is the effective readiness budget for this service: the
	// manifest's ready_timeout, or the 30s default when it is absent. It is
	// normalized as a Go duration string ("30s").
	ReadyTimeout string `json:"ready_timeout"`
	Port         *int   `json:"port" jsonschema:"nullable"`
	// PortAuto is `port: auto`: the daemon assigns the port when it starts the
	// service, so Port is null and PortActual says where it is running.
	PortAuto bool `json:"port_auto"`
	// Health is the declared check in its published form: an HTTP path
	// (/healthz), or tcp:<port>, or command:<command line>.
	Health *string `json:"health" jsonschema:"nullable"`
	// HealthStatus is the latest result for the declared health check.
	// Health remains the manifest value; this field is the runtime verdict so a
	// reader never has to join a service back to a listener to answer whether it
	// is ready.
	HealthStatus *Health `json:"health_status" jsonschema:"nullable"`
	// RuntimeCmd and RuntimeCwd are captured from the live run registry. A
	// mismatch flag makes a manifest edit visible without asking a reader to
	// infer it from process metadata.
	RuntimeCmd              *string `json:"runtime_cmd" jsonschema:"nullable"`
	RuntimeCwd              *string `json:"runtime_cwd" jsonschema:"nullable"`
	ManifestHash            *string `json:"manifest_hash" jsonschema:"nullable"`
	RuntimeSpecHash         *string `json:"runtime_spec_hash" jsonschema:"nullable"`
	ManifestRuntimeMismatch bool    `json:"manifest_runtime_mismatch"`
	// CodeHead is the commit checked out when this run started, when the
	// checkout had one. It is start-time evidence, not the current HEAD.
	CodeHead *string `json:"code_head" jsonschema:"nullable"`
	// CodeChangedSinceStart is filled by `status` for a running service whose
	// start snapshot is available. Null means unknown: the run predates code
	// tracking, the snapshot is gone, or the sources could not be read. Null is
	// never "unchanged".
	CodeChangedSinceStart *bool `json:"code_changed_since_start" jsonschema:"nullable"`
	// CodeChangedFiles is the exact number of files that differ from the start
	// snapshot, when both snapshots kept per-file evidence. Null means unknown,
	// including when a very large source set was fingerprinted by digest only.
	CodeChangedFiles *int    `json:"code_changed_files" jsonschema:"nullable"`
	CodeHint         *string `json:"code_hint" jsonschema:"nullable"`
	// Description, Icon and Color are user-authored metadata from
	// `oberth.yaml` (contract §13.1). The daemon stores and serves them; what
	// an icon or a colour means is the client's business.
	Description *string  `json:"description" jsonschema:"nullable"`
	Icon        *string  `json:"icon" jsonschema:"nullable"`
	Color       *string  `json:"color" jsonschema:"nullable"`
	DependsOn   []string `json:"depends_on"`
	Running     bool     `json:"running"`
	PortActual  *int     `json:"port_actual" jsonschema:"nullable"`
	// PID identifies the observed process when a service is running. RunID and
	// StartedAt identify a live run option-berth owns. They are nullable because
	// a service may be declared but stopped. Keeping these beside Running gives
	// a reader enough evidence to follow a portless worker without consulting a
	// second command.
	PID       *int    `json:"pid" jsonschema:"nullable"`
	RunID     *string `json:"run_id" jsonschema:"nullable"`
	StartedAt *string `json:"started_at" jsonschema:"nullable"`
	// LogPath is where this service's output goes, from the engine's own
	// convention (`paths.ServiceLog`). It is published so a reader never
	// composes the path itself: the client used to keep a copy of the layout,
	// and two copies of a convention drift.
	LogPath *string `json:"log_path" jsonschema:"nullable"`
	// LastExit is how this service's last run ended, for a service option-berth
	// started that is not running now: "api crashed with exit code 1".
	LastExit *ServiceExit `json:"last_exit" jsonschema:"nullable"`
	// Starting is the in-flight start attempt, present only while a
	// `groups.start` is waiting, preparing or spawning this service. It is a
	// transient diagnostic fact owned by the start call: it is never persisted,
	// never a run, and absent before the attempt and after it ends. elapsed_ms
	// is computed when this snapshot is read, not when the attempt changed.
	Starting *ServiceStarting `json:"starting" jsonschema:"nullable"`
}

// ServiceStarting is one in-flight start attempt as a reader sees it. The
// phase is waiting_dependency, prepare, or spawning; started_at is when the
// attempt began, elapsed_ms how long ago that was at read time, ready_timeout
// the effective budget this start uses, waiting_on the dependencies this start
// declared to wait for, and last_prepare_ms the previous prepare duration when
// one is known (advisory evidence, never a decision).
type ServiceStarting struct {
	Phase         string   `json:"phase" jsonschema:"enum=waiting_dependency,enum=prepare,enum=spawning"`
	StartedAt     string   `json:"started_at"`
	ReadyTimeout  string   `json:"ready_timeout"`
	ElapsedMS     int64    `json:"elapsed_ms"`
	WaitingOn     []string `json:"waiting_on"`
	LastPrepareMS *int64   `json:"last_prepare_ms" jsonschema:"nullable"`
}

// ServiceExit is how a run of a service ended. Reason is exited (code 0),
// crashed (any other code), port_occupied (a bind failed because another
// process owns the declared port), start_failed (the command could not be
// started), stopped (option-berth or the user asked it to stop), ready_timeout
// (a readiness wait expired), dependency_timeout (a dependency readiness wait
// expired), or dependency_not_ready (a dependency could not be resolved or
// exited before becoming ready).
type ServiceExit struct {
	Code   int    `json:"code"`
	Reason string `json:"reason" jsonschema:"enum=exited,enum=crashed,enum=port_occupied,enum=start_failed,enum=stopped,enum=ready_timeout,enum=dependency_timeout,enum=dependency_not_ready"`
	At     string `json:"at"`
	RunID  string `json:"run_id"`
	// PortHint is the port this run was told to bind. It survives a later
	// manifest edit and is how port_occupied evidence is found again.
	PortHint int `json:"port_hint,omitempty"`
	// PortHolder is present only for port_occupied, when the declared port was
	// observed held at read time.
	PortHolder *PortHolder `json:"port_holder,omitempty" jsonschema:"nullable"`
}

// ServiceRun identifies a live option-berth-owned service. It intentionally
// carries only runtime evidence needed by status and clients; command and cwd
// remain manifest fields on Service. SpecHash fingerprints the executable
// manifest fields used by this run without carrying environment values.
type ServiceRun struct {
	ID        string `json:"id"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	Cmd       string `json:"cmd"`
	Cwd       string `json:"cwd"`
	SpecHash  string `json:"spec_hash,omitempty"`
}

// MachineRef is one entry of a manifest's top-level `machine:` list: a service
// on this machine that the project depends on. It is a reference, not a member
// — `up` and `down` never touch it (decision 0010) — and Listening is what
// reality says about it, so the declaration and the fact read side by side.
type MachineRef struct {
	Name      string `json:"name"`
	Port      int    `json:"port"`
	Listening bool   `json:"listening"`
	// Unit is the service-manager unit the referenced port resolved to, when it
	// resolved to one: the name that carries that service's other ports.
	Unit *string `json:"unit" jsonschema:"nullable"`
}

// Group is a set of ports that belong to one project. Members are port
// numbers; clients join them against the ports list.
type Group struct {
	// Host names the machine this group lives on (see Port.Host).
	Host string `json:"host"`
	Name string `json:"name"`
	// Repo is the project every checkout of one repository shares: the main
	// checkout's group name. A group that is not a git checkout's is its own
	// project, so Repo equals Name.
	Repo string `json:"repo"`
	// Worktree is a linked worktree's name, the `<worktree>` half of a
	// `<repo>@<worktree>` group. It is empty for a main checkout and for a
	// group that is not a checkout.
	Worktree string `json:"worktree"`
	// Identity is the stable `${worktree}` token of the checkout this group
	// lives in (`wt_` plus twelve hex digits). It is the value a manifest can
	// embed in a database name or data directory, published here so a reader
	// never has to re-derive it from the root path.
	Identity string `json:"identity"`
	// Branch is the branch checked out in the group's checkout, read from its
	// HEAD. It is empty when HEAD is detached or cannot be read.
	Branch     string      `json:"branch"`
	Source     GroupSource `json:"source" jsonschema:"enum=auto,enum=file,enum=start"`
	RootDir    *string     `json:"root_dir" jsonschema:"nullable"`
	ConfigPath *string     `json:"config_path" jsonschema:"nullable"`
	Status     string      `json:"status"` // running | partial | stopped
	Members    []int       `json:"members"`
	Services   []Service   `json:"services"`
	// Machine is what the manifest declares as depending on services of this
	// machine, joined against what is listening. Always an array.
	Machine []MachineRef `json:"machine"`
}

// Key is the stable delta identity for the group.
func (g Group) Key() string { return PrefixKey(g.Host, g.Name) }

// SessionRecord is a Session plus the aggregate counts the sessions collection
// carries.
type SessionRecord struct {
	Session
	Host      string `json:"host"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	Runs      int    `json:"runs"`
	Ports     int    `json:"ports"`
	Groups    int    `json:"groups"`
	Active    bool   `json:"active"`
}

// Key is the delta identity: the session id, namespaced by host.
func (s SessionRecord) Key() string { return PrefixKey(s.Host, s.ID) }
