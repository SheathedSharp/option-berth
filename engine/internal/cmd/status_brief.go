package cmd

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// briefStatusDocument is the `status --json --brief` shape. It is a separate
// document, not the full one with empty fields: the point is that an agent
// reading thirty services does not pay for manifest copies, git history or run
// records it did not ask about.
//
// Everything here is a runtime fact about a declared service. The full document
// remains the place for drafts, the repository summary, the change ledger and
// the attention handoff.
type briefStatusDocument struct {
	Scope       display.Scope          `json:"scope"`
	Worktree    briefWorktree          `json:"worktree"`
	Ports       []int                  `json:"ports"`
	Machine     []briefMachine         `json:"machine,omitempty"`
	Exits       []briefExit            `json:"exits"`
	Observation *rpc.ObservationStatus `json:"observation,omitempty"`
}

type briefWorktree struct {
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Branch   string `json:"branch,omitempty"`
	// ManifestMissing says no oberth.yaml declares this worktree's services, so
	// an empty `services` means "nothing to read", not "nothing declared". The
	// full document publishes the same fact as a null `config_path`; the brief
	// drops that field, and without this an agent cannot tell a checkout waiting
	// for `oberth init` from a configured, idle one. Absent when a manifest
	// exists: an explicit false would be a second spelling of the same silence.
	ManifestMissing bool           `json:"manifest_missing,omitempty"`
	Services        []briefService `json:"services"`
	// AdHocRuns lists every run whose listeners no running service row
	// already names. That includes an `oberth start` run the manifest does not
	// declare, a stale generation still holding a port, and a declared
	// service's run holding an extra listener. `services` cannot show them;
	// without this a brief reader sees only bare port numbers and cannot tell
	// oberth owns the process.
	AdHocRuns []briefAdHocRun `json:"ad_hoc_runs"`
	// UnownedPorts is the remaining listeners: the process behind the port is
	// not an option-berth run. Every port in `ports` is therefore covered by a
	// service row, by `ad_hoc_runs`, or by this list.
	UnownedPorts    []briefUnownedPort `json:"unowned_ports"`
	RunningServices int                `json:"running_services"`
	Resources       *state.Resources   `json:"resources,omitempty"`
}

// briefAdHocRun is one run's attribution: identity, the command it was started
// with, and the listeners it holds. A run may hold several ports; the ports are
// sorted and never null. The command facts come from the run mirror; they are
// absent when the mirror no longer has that generation, never invented.
type briefAdHocRun struct {
	Name      string `json:"name"`
	RunID     string `json:"run_id,omitempty"`
	Group     string `json:"group,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Cmd       string `json:"cmd,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	Ports     []int  `json:"ports"`
}

// briefUnownedPort is a listener no option-berth run owns. The command and pid
// are what a reader needs to identify the process before deciding anything;
// both are absent when the process table could not supply them.
type briefUnownedPort struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid,omitempty"`
	Command string `json:"command,omitempty"`
}

// portsText renders a port list for the human listener line.
func portsText(ports []int) string {
	parts := make([]string, len(ports))
	for i, port := range ports {
		parts[i] = strconv.Itoa(port)
	}
	return strings.Join(parts, ", ")
}

type briefService struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	// Port is the declared port; PortActual is where the service is listening
	// now, including a port assigned to `port: auto`.
	Port       *int `json:"port,omitempty"`
	PortActual *int `json:"port_actual,omitempty"`
	PortAuto   bool `json:"port_auto,omitempty"`
	// PortHolder is present when this service is running, but the listener on
	// PortActual is not this service's own run: another run's listener, or a
	// process option-berth did not start. It is the same shape the failure
	// record publishes. Absent means the listener was this service's run or the
	// snapshot did not explain it, never that the port is free.
	PortHolder *state.PortHolder `json:"port_holder,omitempty"`
	Health     *briefHealth      `json:"health,omitempty"`
	// ExitReason and ExitCode are the service's current last_exit. Hint is the
	// deterministic next command, when that exit left a log to read.
	ExitReason string      `json:"exit_reason,omitempty"`
	ExitCode   *int        `json:"exit_code,omitempty"`
	Hint       string      `json:"hint,omitempty"`
	Stats      *briefStats `json:"stats,omitempty"`
	// ManifestRuntimeMismatch is kept because it is the one cheap signal that
	// the process is running an older manifest; it is omitted when false.
	ManifestRuntimeMismatch bool `json:"manifest_runtime_mismatch,omitempty"`
	// CodeChangedSinceStart is the same fact the full document publishes:
	// true/false when checked, absent when unknown. CodeChangedFiles and
	// CodeHint accompany it only when the source changed.
	CodeChangedSinceStart *bool  `json:"code_changed_since_start,omitempty"`
	CodeChangedFiles      *int   `json:"code_changed_files,omitempty"`
	CodeHint              string `json:"code_hint,omitempty"`
	// The executable facts answer the compiled-artifact question for a
	// directly-run binary; absent means unknown, never "unchanged".
	ExecutablePath              *string `json:"executable_path,omitempty"`
	ExecutableModifiedAt        *string `json:"executable_modified_at,omitempty"`
	ExecutableChangedSinceStart *bool   `json:"executable_changed_since_start,omitempty"`
	// Starting is the in-flight start attempt, the same transient fact the
	// full document carries. A brief reader often reads exactly while services
	// are coming up, and "not running" alone cannot say whether that is a wait
	// in progress or a service that never started. Its own copy, so the brief
	// document never aliases the snapshot's slice.
	Starting *briefStarting `json:"starting,omitempty"`
}

// briefStarting mirrors state.ServiceStarting field for field. The projection
// copies rather than aliases: the brief document is encoded after the snapshot
// is released, and it must never share a mutable slice with the full one.
type briefStarting struct {
	Phase        string `json:"phase"`
	StartedAt    string `json:"started_at"`
	ElapsedMS    int64  `json:"elapsed_ms"`
	ReadyTimeout string `json:"ready_timeout"`
	// WaitingOn is always an array and LastPrepareMS is null when unknown: the
	// same wire shape state.ServiceStarting publishes. A brief reader must not
	// have to distinguish "absent" from "empty" on a field the full document
	// always carries.
	WaitingOn     []string `json:"waiting_on"`
	LastPrepareMS *int64   `json:"last_prepare_ms"`
}

type briefHealth struct {
	Status string `json:"status"`
	Code   int    `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// briefStats is the resource half worth carrying per service. Uptime, state and
// connection counts stay in the full document.
type briefStats struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemoryRSS  int64   `json:"memory_rss_bytes"`
}

type briefMachine struct {
	Name           string `json:"name"`
	Port           int    `json:"port"`
	Listening      bool   `json:"listening"`
	ListeningKnown bool   `json:"listening_known"`
}

type briefExit struct {
	RunID    string `json:"run_id,omitempty"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
	Code     *int   `json:"code,omitempty"`
	ExitedAt string `json:"exited_at,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

// briefStartingFrom copies one attempt. WaitingOn and LastPrepareMS are new
// values, never aliases of the snapshot the attempt came from.
func briefStartingFrom(in *state.ServiceStarting) *briefStarting {
	if in == nil {
		return nil
	}
	out := &briefStarting{
		Phase:        in.Phase,
		StartedAt:    in.StartedAt,
		ElapsedMS:    in.ElapsedMS,
		ReadyTimeout: in.ReadyTimeout,
		// Always a fresh non-nil slice, so an empty set encodes as [] exactly as
		// the full document does and never aliases the snapshot's slice.
		WaitingOn: append([]string{}, in.WaitingOn...),
	}
	if in.LastPrepareMS != nil {
		ms := *in.LastPrepareMS
		out.LastPrepareMS = &ms
	}
	return out
}

func briefStatusFrom(doc statusDocument) briefStatusDocument {
	adHoc, unowned := listenerAttribution(doc)
	out := briefStatusDocument{
		Scope: doc.Scope,
		Worktree: briefWorktree{
			Name:            doc.Worktree.Name,
			Identity:        doc.Worktree.Identity,
			Branch:          doc.Worktree.Branch,
			ManifestMissing: doc.manifestMissing,
			Services:        make([]briefService, 0, len(doc.Worktree.Services)),
			AdHocRuns:       adHoc,
			UnownedPorts:    unowned,
			RunningServices: doc.Worktree.RunningServices,
			Resources:       doc.Worktree.Resources,
		},
		Ports:       make([]int, 0, len(doc.Ports)),
		Exits:       make([]briefExit, 0, len(doc.Exits)),
		Observation: doc.Observation,
	}

	for _, port := range doc.Ports {
		out.Ports = append(out.Ports, port.Port)
	}
	for _, machine := range doc.Worktree.Machine {
		out.Machine = append(out.Machine, briefMachine{
			Name: machine.Name, Port: machine.Port, Listening: machine.Listening,
			ListeningKnown: machine.ListeningKnown,
		})
	}

	hints := make(map[string]string, len(doc.Exits))
	for _, run := range doc.Exits {
		if run.Hint != "" {
			hints[run.ID] = run.Hint
		}
	}

	for _, svc := range doc.Worktree.Services {
		item := briefService{
			Name: svc.Name, Running: svc.Running,
			Port: svc.Port, PortActual: svc.PortActual, PortAuto: svc.PortAuto,
			PortHolder:                  briefUnownedPortHolder(doc, svc),
			ManifestRuntimeMismatch:     svc.ManifestRuntimeMismatch,
			CodeChangedSinceStart:       svc.CodeChangedSinceStart,
			CodeChangedFiles:            svc.CodeChangedFiles,
			ExecutablePath:              svc.ExecutablePath,
			ExecutableModifiedAt:        svc.ExecutableModifiedAt,
			ExecutableChangedSinceStart: svc.ExecutableChangedSinceStart,
		}
		if svc.CodeHint != nil {
			item.CodeHint = *svc.CodeHint
		}
		if svc.HealthStatus != nil {
			item.Health = &briefHealth{
				Status: svc.HealthStatus.Status,
				Code:   svc.HealthStatus.Code,
				Reason: svc.HealthStatus.Reason,
			}
		}
		if svc.Stats != nil {
			item.Stats = &briefStats{
				CPUPercent: svc.Stats.CPUPercent,
				MemoryRSS:  svc.Stats.MemoryRSS,
			}
		}
		if svc.Starting != nil {
			item.Starting = briefStartingFrom(svc.Starting)
		}
		if svc.LastExit != nil {
			item.ExitReason = svc.LastExit.Reason
			code := svc.LastExit.Code
			item.ExitCode = &code
			item.Hint = hints[svc.LastExit.RunID]
			if item.Hint == "" {
				// A legacy exit may not carry a run ID; the newest exit for the
				// same service is the same fact for this purpose.
				for _, run := range doc.Exits {
					if run.Name == svc.Name && run.Hint != "" {
						item.Hint = run.Hint
						break
					}
				}
			}
		}
		out.Worktree.Services = append(out.Worktree.Services, item)
	}

	for _, run := range doc.Exits {
		item := briefExit{
			RunID: run.ID, Name: run.Name, Reason: run.Reason,
			ExitedAt: run.ExitedAt, Hint: run.Hint,
		}
		if run.ExitCode != nil {
			code := *run.ExitCode
			item.Code = &code
		}
		out.Exits = append(out.Exits, item)
	}

	return out
}

// briefUnownedPortHolder says whether the listener behind a running service is
// some other process. The brief document already carries the port; without this
// a reader cannot tell a service option-berth started from a foreign process
// that happens to hold the declared port, and the full document's `ports[].run`
// is exactly the join the brief view exists to spare the reader.
//
// It returns nil when the listener belongs to this service's own run, or when
// the snapshot did not explain the port. A nil answer is not proof of
// ownership; it means the brief has no holder to report.
func briefUnownedPortHolder(doc statusDocument, svc state.Service) *state.PortHolder {
	if !svc.Running || svc.PortActual == nil {
		return nil
	}
	listener := listenerOnPort(doc.Ports, *svc.PortActual)
	if listener == nil {
		return nil
	}
	// The service's live run is the only owner that makes this its own
	// listener. A listener attributed to an earlier generation, another run or
	// no run at all is evidence the port is not this run's.
	if owner := listener.Run; owner != nil && svc.RunID != nil && owner.ID == *svc.RunID {
		return nil
	}
	return servicefacts.FindPortHolder(doc.Ports, servicefacts.PortHolderQuery{
		Port:    *svc.PortActual,
		Group:   doc.Worktree.Name,
		Service: svc.Name,
	})
}

// listenerOnPort is the row the service's PortActual resolved against. The
// first row wins, matching the resolver's input-order tie-breaker.
func listenerOnPort(ports []state.Port, port int) *state.Port {
	for i := range ports {
		if ports[i].Port == port {
			return &ports[i]
		}
	}
	return nil
}

// briefAdHocRuns groups the listeners owned by runs the manifest does not
// declare. The run id is the identity; a record without one falls back to the
// name. Declared services stay in `services`, where their port already lives.
// listenerAttribution gives every listener in `ports` exactly one home:
// covered by a running service row, owned by another run, or unowned. The old
// split by service name hid a stale generation that still used a declared
// name and left every other listener as a bare number; attribution has to
// follow the run identity, not the label.
//
// A live run that holds no listener is still a run option-berth owns: `down`
// stops it and `restart --run` replays it, so leaving it out of every section
// makes an `oberth start -d` worker reachable only by scanning `ps`. Those rows
// carry an empty `ports`.
func listenerAttribution(doc statusDocument) ([]briefAdHocRun, []briefUnownedPort) {
	covered := make(map[int]bool, len(doc.Worktree.Services))
	for _, svc := range doc.Worktree.Services {
		if svc.Running && svc.PortActual != nil {
			covered[*svc.PortActual] = true
		}
	}
	byRun := make(map[string]*briefAdHocRun)
	unowned := make([]briefUnownedPort, 0)
	seenUnowned := make(map[int]bool)
	for _, port := range doc.Ports {
		if covered[port.Port] {
			continue
		}
		run := port.Run
		if run == nil {
			if seenUnowned[port.Port] {
				continue
			}
			seenUnowned[port.Port] = true
			unowned = append(unowned, briefUnownedPort{
				Port: port.Port, PID: port.PID, Command: port.Command,
			})
			continue
		}
		item, ok := byRun[adHocRunKey(run.ID, run.Group, run.RootPID)]
		if !ok {
			item = &briefAdHocRun{
				Name: run.Name, RunID: run.ID, Group: run.Group, PID: run.RootPID,
				Ports: []int{},
			}
			byRun[adHocRunKey(run.ID, run.Group, run.RootPID)] = item
		}
		if !slices.Contains(item.Ports, port.Port) {
			item.Ports = append(item.Ports, port.Port)
		}
	}

	// The run's start facts are on the run record, not in the listener row: the
	// listener's own command is often a child (`node …`) rather than the
	// recorded start line. Join by pid and generation, never by name.
	for _, item := range byRun {
		record, ok := recordedRun(doc.Runs, item.RunID, item.PID)
		if !ok {
			continue
		}
		item.Cmd = record.Cmd
		item.Cwd = record.Cwd
		item.StartedAt = normalizeStartedAt(record.StartedAt)
	}
	addLiveRunsWithoutListeners(doc, byRun)

	out := make([]briefAdHocRun, 0, len(byRun))
	for _, item := range byRun {
		sort.Ints(item.Ports)
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].RunID < out[j].RunID
	})
	sort.Slice(unowned, func(i, j int) bool { return unowned[i].Port < unowned[j].Port })
	return out, unowned
}

// adHocRunKey is the identity a listener row and a run record are joined on.
// The run id is the real identity; a row from a producer that publishes none
// falls back to the root process, which is the only evidence it carries.
func adHocRunKey(runID, group string, rootPID int) string {
	if runID != "" {
		return runID
	}
	return fmt.Sprintf("%s/%d", group, rootPID)
}

// recordedRun is the run record a listener row belongs to. The run id is the
// identity: a row that names one is answered only by that generation, because a
// recycled pid belongs to some later process. A row that carries no id is
// answered by its root pid, the only evidence it has.
func recordedRun(records []rpc.RunRecord, runID string, rootPID int) (rpc.RunRecord, bool) {
	if runID != "" {
		for _, record := range records {
			if record.ID == runID {
				return record, true
			}
		}
		return rpc.RunRecord{}, false
	}
	if rootPID <= 0 {
		return rpc.RunRecord{}, false
	}
	for _, record := range records {
		if record.PID == rootPID {
			return record, true
		}
	}
	return rpc.RunRecord{}, false
}

// addLiveRunsWithoutListeners fills in the runs this worktree started that hold
// no listener, so they are attributable from the brief document instead of only
// from `ps`. A run a running service row already names stays in `services`: the
// row is its home and repeating it here would publish one process twice.
//
// doc.Runs is already this worktree's live set, resolved once for both documents,
// so there is no group filter and no second source here — a projection reads the
// document it was given.
func addLiveRunsWithoutListeners(doc statusDocument, byRun map[string]*briefAdHocRun) {
	named := make(map[string]bool, len(doc.Worktree.Services))
	for _, svc := range doc.Worktree.Services {
		if svc.RunID != nil && *svc.RunID != "" {
			named[*svc.RunID] = true
		}
	}
	for _, record := range doc.Runs {
		key := adHocRunKey(record.ID, record.Group, record.PID)
		if _, seen := byRun[key]; seen || named[record.ID] {
			continue
		}
		byRun[key] = &briefAdHocRun{
			Name:      record.Name,
			RunID:     record.ID,
			Group:     record.Group,
			PID:       record.PID,
			Cmd:       record.Cmd,
			Cwd:       record.Cwd,
			StartedAt: normalizeStartedAt(record.StartedAt),
			Ports:     []int{},
		}
	}
}
