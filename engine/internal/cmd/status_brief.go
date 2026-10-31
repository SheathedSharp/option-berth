package cmd

import (
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
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
	Name            string           `json:"name"`
	Identity        string           `json:"identity"`
	Branch          string           `json:"branch,omitempty"`
	Services        []briefService   `json:"services"`
	RunningServices int              `json:"running_services"`
	Resources       *state.Resources `json:"resources,omitempty"`
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
	// Starting is the transient start attempt. It is a fact about why a
	// service is not ready yet, so the brief view must not hide it.
	Starting *state.ServiceStarting `json:"starting,omitempty"`
	// ManifestRuntimeMismatch is kept because it is the one cheap signal that
	// the process is running an older manifest; it is omitted when false.
	ManifestRuntimeMismatch bool `json:"manifest_runtime_mismatch,omitempty"`
	// CodeChangedSinceStart is the same fact the full document publishes:
	// true/false when checked, absent when unknown. CodeChangedFiles and
	// CodeHint accompany it only when the source changed.
	CodeChangedSinceStart *bool  `json:"code_changed_since_start,omitempty"`
	CodeChangedFiles      *int   `json:"code_changed_files,omitempty"`
	CodeHint              string `json:"code_hint,omitempty"`
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
	Name      string `json:"name"`
	Port      int    `json:"port"`
	Listening bool   `json:"listening"`
}

type briefExit struct {
	RunID    string `json:"run_id,omitempty"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
	Code     *int   `json:"code,omitempty"`
	ExitedAt string `json:"exited_at,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

func briefStatusFrom(doc statusDocument) briefStatusDocument {
	out := briefStatusDocument{
		Scope: doc.Scope,
		Worktree: briefWorktree{
			Name:            doc.Worktree.Name,
			Identity:        doc.Worktree.Identity,
			Branch:          doc.Worktree.Branch,
			Services:        make([]briefService, 0, len(doc.Worktree.Services)),
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
			ManifestRuntimeMismatch: svc.ManifestRuntimeMismatch,
			CodeChangedSinceStart:   svc.CodeChangedSinceStart,
			CodeChangedFiles:        svc.CodeChangedFiles,
			Starting:                svc.Starting,
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
