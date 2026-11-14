package cmd

import (
	"slices"
	"sort"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
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
	Name     string         `json:"name"`
	Identity string         `json:"identity"`
	Branch   string         `json:"branch,omitempty"`
	Services []briefService `json:"services"`
	// AdHocRuns lists `oberth start` runs whose listener belongs to this
	// worktree but whose name is not a declared service. `services` cannot show
	// them; without this a brief reader sees only bare port numbers and cannot
	// tell oberth owns the process.
	AdHocRuns       []briefAdHocRun  `json:"ad_hoc_runs"`
	RunningServices int              `json:"running_services"`
	Resources       *state.Resources `json:"resources,omitempty"`
}

// briefAdHocRun is one run's attribution: identity plus the listeners it holds.
// A run may hold several ports; the ports are sorted and never null.
type briefAdHocRun struct {
	Name  string `json:"name"`
	RunID string `json:"run_id,omitempty"`
	Group string `json:"group,omitempty"`
	PID   int    `json:"pid,omitempty"`
	Ports []int  `json:"ports"`
}

type briefService struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	// Port is the declared port; PortActual is where the service is listening
	// now, including a port assigned to `port: auto`.
	Port       *int         `json:"port,omitempty"`
	PortActual *int         `json:"port_actual,omitempty"`
	PortAuto   bool         `json:"port_auto,omitempty"`
	Health     *briefHealth `json:"health,omitempty"`
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
	out := briefStatusDocument{
		Scope: doc.Scope,
		Worktree: briefWorktree{
			Name:            doc.Worktree.Name,
			Identity:        doc.Worktree.Identity,
			Branch:          doc.Worktree.Branch,
			Services:        make([]briefService, 0, len(doc.Worktree.Services)),
			AdHocRuns:       briefAdHocRuns(doc),
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

// briefAdHocRuns groups the listeners owned by runs the manifest does not
// declare. The run id is the identity; a record without one falls back to the
// name. Declared services stay in `services`, where their port already lives.
func briefAdHocRuns(doc statusDocument) []briefAdHocRun {
	declared := make(map[string]bool, len(doc.Worktree.Services))
	for _, svc := range doc.Worktree.Services {
		declared[svc.Name] = true
	}
	byRun := make(map[string]*briefAdHocRun)
	for _, port := range doc.Ports {
		run := port.Run
		if run == nil || declared[run.Name] {
			continue
		}
		key := run.ID
		if key == "" {
			key = run.Name
		}
		item, ok := byRun[key]
		if !ok {
			item = &briefAdHocRun{
				Name: run.Name, RunID: run.ID, Group: run.Group, PID: run.RootPID,
				Ports: []int{},
			}
			byRun[key] = item
		}
		if !slices.Contains(item.Ports, port.Port) {
			item.Ports = append(item.Ports, port.Port)
		}
	}
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
	return out
}
