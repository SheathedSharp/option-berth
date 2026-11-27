package runtimecore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// WorldSchema is the version tag of the Runtime World this package reduces.
const WorldSchema = "oberth.runtime.world/v1"

// WorldHealth is the one-word verdict about a worktree's runtime.
type WorldHealth string

const (
	WorldHealthy  WorldHealth = "healthy"
	WorldStarting WorldHealth = "starting"
	WorldDrifted  WorldHealth = "drifted"
	WorldDegraded WorldHealth = "degraded"
	WorldBroken   WorldHealth = "broken"
	WorldIdle     WorldHealth = "idle"
	WorldBlind    WorldHealth = "blind"
)

// Coverage says how much of the listener table this world could see. A
// narrow or partial table is not an empty one: a port missing from it is
// unknown, never free.
const (
	CoverageObservable = "observable"
	CoveragePartial    = "partial"
	CoverageBlind      = "blind"
)

// ServiceLifecycleState is where one declared service is in its life.
type ServiceLifecycleState string

const (
	ServiceReady        ServiceLifecycleState = "ready"
	ServiceStarting     ServiceLifecycleState = "starting"
	ServiceDegraded     ServiceLifecycleState = "degraded"
	ServiceCrashed      ServiceLifecycleState = "crashed"
	ServiceCrashLoop    ServiceLifecycleState = "crash_loop"
	ServicePortConflict ServiceLifecycleState = "port_conflict"
	ServiceBlockedByDep ServiceLifecycleState = "blocked_by_dep"
	ServiceStopped      ServiceLifecycleState = "stopped"
	ServiceUnobserved   ServiceLifecycleState = "unobserved"
)

// DriftKind names one way reality has moved away from what the manifest asked
// for. The first three are non-fatal: the process works, but not from the code
// or declaration the person last wrote.
type DriftKind string

const (
	DriftCodeStale         DriftKind = "code_stale"
	DriftBinaryStale       DriftKind = "binary_stale"
	DriftManifestStale     DriftKind = "manifest_stale"
	DriftPortOccupied      DriftKind = "port_occupied"
	DriftUnownedListener   DriftKind = "unowned_listener"
	DriftHealthFailing     DriftKind = "health_failing"
	DriftReadyTimeout      DriftKind = "ready_timeout"
	DriftServiceCrashed    DriftKind = "service_crashed"
	DriftStartFailed       DriftKind = "start_failed"
	DriftRepeatedCrash     DriftKind = "repeated_crash"
	DriftDependencyBlocked DriftKind = "dependency_blocked"
)

// Severity ranks what a reader should do about a fact.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Safety says what kind of act the next command performs.
const (
	SafetyReadOnly      = "read_only"
	SafetyLocalRestart  = "local_restart"
	SafetyHumanDecision = "human_decision"
)

// WorldInput is the complete, captured evidence one reduction reads. The
// reducer never observes the OS: a caller that has scanned already hands the
// facts over here.
type WorldInput struct {
	Seq         uint64             `json:"seq"`
	ObservedAt  string             `json:"observed_at,omitempty"`
	Group       state.Group        `json:"group"`
	Ports       []state.Port       `json:"ports,omitempty"`
	Observation *state.Observation `json:"observation,omitempty"`
	ActiveRuns  []state.ServiceRun `json:"active_runs,omitempty"`
	Exits       []ExitEvidence     `json:"exits,omitempty"`
	FirstErrors map[string]string  `json:"first_errors,omitempty"`
	Loops       []LoopSignal       `json:"loops,omitempty"`
}

// ExitEvidence is one ended run as a reader can still see it. FirstError is
// already bounded to one line by the caller that read the log.
type ExitEvidence struct {
	Service    string `json:"service"`
	RunID      string `json:"run_id,omitempty"`
	Reason     string `json:"reason"`
	Code       int    `json:"code"`
	At         string `json:"at,omitempty"`
	LogPath    string `json:"log_path,omitempty"`
	FirstError string `json:"first_error,omitempty"`
}

// WorldIdentity names the checkout this world belongs to.
type WorldIdentity struct {
	Identity        string `json:"identity,omitempty"`
	Group           string `json:"group"`
	Root            string `json:"root,omitempty"`
	Branch          string `json:"branch,omitempty"`
	ConfigPath      string `json:"config_path,omitempty"`
	ManifestPresent bool   `json:"manifest_present"`
}

// World is the causal projection of one worktree: what the manifest intends,
// what the runs are doing, what the listener table shows, and what follows
// from the three together.
type World struct {
	Schema     string             `json:"schema"`
	Seq        uint64             `json:"seq"`
	ObservedAt string             `json:"observed_at,omitempty"`
	Revision   string             `json:"revision"`
	Worktree   WorldIdentity      `json:"worktree"`
	Coverage   string             `json:"coverage"`
	Health     WorldHealth        `json:"health"`
	Services   []ServiceRuntime   `json:"services"`
	Machine    []state.MachineRef `json:"machine,omitempty"`
	Diagnosis  CausalDiagnosis    `json:"diagnosis"`
}

// ServiceRuntime is one declared service, its observed state, and the
// individual drifts found on it.
type ServiceRuntime struct {
	Name         string                 `json:"name"`
	State        ServiceLifecycleState  `json:"state"`
	Ready        bool                   `json:"ready"`
	Running      bool                   `json:"running"`
	RunID        string                 `json:"run_id,omitempty"`
	PID          int                    `json:"pid,omitempty"`
	PortDeclared *int                   `json:"port_declared,omitempty"`
	PortActual   *int                   `json:"port_actual,omitempty"`
	PortAuto     bool                   `json:"port_auto,omitempty"`
	DependsOn    []string               `json:"depends_on,omitempty"`
	BlockedBy    []string               `json:"blocked_by,omitempty"`
	Health       *state.Health          `json:"health,omitempty"`
	Starting     *state.ServiceStarting `json:"starting,omitempty"`
	LastExit     *state.ServiceExit     `json:"last_exit,omitempty"`
	PortHolder   *state.PortHolder      `json:"port_holder,omitempty"`
	Drifts       []Drift                `json:"drifts,omitempty"`
	FirstError   string                 `json:"first_error,omitempty"`
	LogPath      string                 `json:"log_path,omitempty"`
}

// Drift is one fact that does not match the declaration. Command, when set, is
// the exact next oberth command that addresses it.
type Drift struct {
	Kind     DriftKind `json:"kind"`
	Severity Severity  `json:"severity"`
	Summary  string    `json:"summary"`
	Command  string    `json:"command,omitempty"`
}

// RootCause is the service where a failure actually began. Impacted lists the
// downstream services whose failure is explained by it; those services do not
// get a duplicate root cause of their own.
type RootCause struct {
	ID         string    `json:"id"`
	Service    string    `json:"service"`
	Kind       DriftKind `json:"kind"`
	Severity   Severity  `json:"severity"`
	Summary    string    `json:"summary"`
	Impacted   []string  `json:"impacted,omitempty"`
	Evidence   []string  `json:"evidence,omitempty"`
	FirstError string    `json:"first_error,omitempty"`
	Command    string    `json:"command,omitempty"`
}

// NextAction is one ordered step a reader may take.
type NextAction struct {
	Priority int    `json:"priority"`
	Safety   string `json:"safety"`
	Service  string `json:"service,omitempty"`
	Summary  string `json:"summary"`
	Command  string `json:"command"`
}

// CausalDiagnosis is the root-cause half of a world: why it is not healthy,
// which services that explains, and what to do first.
type CausalDiagnosis struct {
	Healthy     bool         `json:"healthy"`
	Summary     string       `json:"summary"`
	RootCauses  []RootCause  `json:"root_causes,omitempty"`
	Drifts      []Drift      `json:"drifts,omitempty"`
	Loops       []LoopSignal `json:"loops,omitempty"`
	NextActions []NextAction `json:"next_actions,omitempty"`
}

// ReduceWorld is the deterministic reducer behind every Runtime World: the same
// input yields the same output, and it performs no OS, network or clock read.
func ReduceWorld(in WorldInput) World {
	world := World{
		Schema:     WorldSchema,
		Seq:        in.Seq,
		ObservedAt: in.ObservedAt,
		Worktree:   identityOf(in.Group),
		Coverage:   coverageOf(in.Observation),
	}
	world.Machine = cloneMachine(in.Group.Machine)

	services := make([]ServiceRuntime, 0, len(in.Group.Services))
	for _, svc := range in.Group.Services {
		services = append(services, reduceService(in, svc, world.Coverage))
	}
	world.Services = services
	world.Diagnosis = reduceDiagnosis(in, services)
	world.Health = worldHealth(services, world.Diagnosis, world.Coverage)
	world.Diagnosis.Healthy = diagnosisHealthy(world.Health, world.Diagnosis, services, world.Coverage)
	if world.Diagnosis.Summary == "" {
		world.Diagnosis.Summary = diagnosisSummary(world.Health, world.Diagnosis, services)
	}
	world.Revision = revisionOf(in)
	return world
}

func identityOf(group state.Group) WorldIdentity {
	root, config := "", ""
	if group.RootDir != nil {
		root = *group.RootDir
	}
	if group.ConfigPath != nil {
		config = *group.ConfigPath
	}
	return WorldIdentity{
		Identity:        group.Identity,
		Group:           group.Name,
		Root:            root,
		Branch:          group.Branch,
		ConfigPath:      config,
		ManifestPresent: config != "",
	}
}

// coverageOf reads only the observation. A nil observation is the healthy wire
// shape (the read path omits it when the table is whole), so it means
// observable rather than unknown.
func coverageOf(obs *state.Observation) string {
	if obs == nil {
		return CoverageObservable
	}
	switch obs.State {
	case "", state.ObservationObservable:
		if obs.Narrow() {
			return CoveragePartial
		}
		return CoverageObservable
	case state.ObservationBlind, state.ObservationBindDenied:
		return CoverageBlind
	default:
		return CoveragePartial
	}
}

func reduceService(in WorldInput, svc state.Service, coverage string) ServiceRuntime {
	sr := ServiceRuntime{
		Name:         svc.Name,
		Running:      svc.Running,
		PortDeclared: cloneInt(svc.Port),
		PortActual:   cloneInt(svc.PortActual),
		PortAuto:     svc.PortAuto,
		DependsOn:    cloneStrings(svc.DependsOn),
		Health:       cloneHealth(svc.HealthStatus),
		Starting:     cloneStarting(svc.Starting),
		LastExit:     cloneExit(svc.LastExit),
	}
	if svc.RunID != nil {
		sr.RunID = *svc.RunID
	}
	if svc.PID != nil {
		sr.PID = *svc.PID
	}
	if svc.LogPath != nil {
		sr.LogPath = *svc.LogPath
	}
	ready, _ := Ready(in.Group, in.Ports, svc.Name)

	exit := latestExit(in.Exits, svc.Name)
	if sr.LastExit == nil && exit != nil {
		sr.LastExit = &state.ServiceExit{Code: exit.Code, Reason: exit.Reason, At: exit.At, RunID: exit.RunID}
	}
	if sr.FirstError == "" {
		sr.FirstError = firstErrorFor(in, svc.Name)
	}
	if sr.LogPath == "" && exit != nil {
		sr.LogPath = exit.LogPath
	}
	if sr.Running && sr.PortActual != nil {
		holder := servicefacts.FindPortHolder(in.Ports, servicefacts.PortHolderQuery{
			Port: *sr.PortActual, Group: in.Group.Name, Service: svc.Name, RunID: sr.RunID,
		})
		if holder != nil && !holderIsSelf(holder, in.Group.Name, svc.Name, sr.RunID) {
			sr.PortHolder = holder
		}
	}
	if !sr.Running && sr.LastExit != nil && sr.LastExit.Reason == "port_occupied" {
		if sr.LastExit.PortHolder != nil {
			sr.PortHolder = cloneHolder(sr.LastExit.PortHolder)
		} else if sr.LastExit.PortHint > 0 {
			sr.PortHolder = servicefacts.FindPortHolder(in.Ports, servicefacts.PortHolderQuery{
				Port: sr.LastExit.PortHint, Group: in.Group.Name, Service: svc.Name, RunID: sr.LastExit.RunID,
			})
		}
	}
	// A listener this run does not own is not readiness, even when the health
	// probe answered: the verdict is about the declared service, and the thing
	// answering is not it.
	sr.Ready = ready && sr.PortHolder == nil && sr.Running

	loop := loopFor(in.Loops, svc.Name)
	sr.State = lifecycleState(sr, coverage, loop)
	sr.Drifts = driftsFor(sr, svc, loop)
	return sr
}

// holderIsSelf is the only case where a listener is this service's own run.
func holderIsSelf(holder *state.PortHolder, group, service, runID string) bool {
	return holder != nil && runID != "" && holder.RunID == runID &&
		holder.Group == group && holder.Name == service
}

// lifecycleState reads the positive facts first: a running process, then an
// in-flight start, then the recorded ending, and only then the absence that a
// degraded table cannot confirm.
func lifecycleState(sr ServiceRuntime, coverage string, loop *LoopSignal) ServiceLifecycleState {
	if loop != nil && loop.Kind == LoopCrashLoop {
		return ServiceCrashLoop
	}
	if sr.Running {
		if sr.PortHolder != nil {
			return ServiceDegraded
		}
		if sr.Ready {
			return ServiceReady
		}
		if sr.Health != nil && sr.Health.Status == state.HealthFail {
			return ServiceDegraded
		}
		return ServiceStarting
	}
	if sr.Starting != nil {
		return ServiceStarting
	}
	if sr.LastExit != nil {
		switch sr.LastExit.Reason {
		case "port_occupied":
			return ServicePortConflict
		case "dependency_not_ready", "dependency_timeout":
			return ServiceBlockedByDep
		case "crashed", "start_failed", "ready_timeout":
			return ServiceCrashed
		case state.ExitEndedUnobserved:
			return ServiceUnobserved
		case "stopped", "exited":
			return ServiceStopped
		default:
			return ServiceCrashed
		}
	}
	if coverage != CoverageObservable {
		return ServiceUnobserved
	}
	return ServiceStopped
}

func driftsFor(sr ServiceRuntime, svc state.Service, loop *LoopSignal) []Drift {
	var out []Drift
	if sr.Running {
		if svc.CodeChangedSinceStart != nil && *svc.CodeChangedSinceStart {
			out = append(out, Drift{
				Kind: DriftCodeStale, Severity: SeverityWarning,
				Summary: "code changed since this run started",
				Command: "oberth restart --only " + svc.Name,
			})
		}
		if svc.ExecutableChangedSinceStart != nil && *svc.ExecutableChangedSinceStart {
			out = append(out, Drift{
				Kind: DriftBinaryStale, Severity: SeverityWarning,
				Summary: "executable is newer than this run",
				Command: "oberth restart --only " + svc.Name,
			})
		}
		if svc.ManifestRuntimeMismatch {
			out = append(out, Drift{
				Kind: DriftManifestStale, Severity: SeverityWarning,
				Summary: "manifest changed since this run started",
				Command: "oberth restart --only " + svc.Name,
			})
		}
		if sr.PortHolder != nil {
			out = append(out, Drift{
				Kind: DriftUnownedListener, Severity: SeverityHigh,
				Summary: "listener on port " + portText(sr.PortActual) + " is not this run",
				Command: "oberth status --json",
			})
		}
		if sr.Health != nil && sr.Health.Status == state.HealthFail {
			out = append(out, Drift{
				Kind: DriftHealthFailing, Severity: SeverityHigh,
				Summary: "health check is failing" + healthReasonText(sr.Health),
				Command: "oberth logs " + svc.Name + " --once",
			})
		}
	}
	if !sr.Running && sr.LastExit != nil {
		switch sr.LastExit.Reason {
		case "port_occupied":
			command, _ := portConflictRemediation(sr)
			out = append(out, Drift{
				Kind: DriftPortOccupied, Severity: SeverityCritical,
				Summary: portConflictSummary(sr), Command: command,
			})
		case "ready_timeout":
			out = append(out, Drift{
				Kind: DriftReadyTimeout, Severity: SeverityCritical,
				Summary: "readiness wait expired", Command: "oberth logs " + svc.Name + " --once",
			})
		case "crashed":
			if loop == nil || loop.Kind != LoopCrashLoop {
				out = append(out, Drift{
					Kind: DriftServiceCrashed, Severity: SeverityCritical,
					Summary: "crashed with exit code " + fmt.Sprint(sr.LastExit.Code),
					Command: "oberth logs " + svc.Name + " --once",
				})
			}
		case "start_failed":
			out = append(out, Drift{
				Kind: DriftStartFailed, Severity: SeverityCritical,
				Summary: "the command could not be started", Command: "oberth logs " + svc.Name + " --once",
			})
		case "dependency_not_ready", "dependency_timeout":
			out = append(out, Drift{
				Kind: DriftDependencyBlocked, Severity: SeverityWarning,
				Summary: "a dependency was not ready", Command: "oberth status --json",
			})
		}
	}
	if loop != nil && loop.Kind == LoopCrashLoop {
		summary := loop.Summary
		if summary == "" {
			summary = fmt.Sprintf("%d repeated crashes in this window", loop.Count)
		}
		out = append(out, Drift{
			Kind: DriftRepeatedCrash, Severity: SeverityCritical,
			Summary: summary, Command: "oberth logs " + svc.Name + " --once",
		})
	}
	return out
}

func portText(port *int) string {
	if port == nil {
		return "?"
	}
	return fmt.Sprint(*port)
}

func healthReasonText(health *state.Health) string {
	if health == nil || health.Reason == "" {
		return ""
	}
	return " (" + health.Reason + ")"
}

// reduceDiagnosis collapses dependency cascades into the fault that started
// them, then ranks what remains into root causes, non-fatal drifts and next
// actions.
func reduceDiagnosis(in WorldInput, services []ServiceRuntime) CausalDiagnosis {
	index := make(map[string]int, len(services))
	for i := range services {
		index[services[i].Name] = i
	}
	for i := range services {
		// BlockedBy is a claim about this service, not about the worktree:
		// a ready service whose dependency happens to be down is not blocked,
		// it is serving. A start in flight only names a dependency it is
		// actually waiting on.
		ancestors := faultedAncestors(services[i].Name, in.Group, services, index)
		if dependencyExplains(services[i], ancestors) {
			sort.Strings(ancestors)
			services[i].BlockedBy = ancestors
		}
	}

	impacted := make(map[string][]string)
	var roots []RootCause
	for i := range services {
		sr := services[i]
		if len(sr.BlockedBy) > 0 {
			for _, ancestor := range sr.BlockedBy {
				impacted[ancestor] = appendUnique(impacted[ancestor], sr.Name)
			}
			continue
		}
		if root, ok := rootCauseFor(sr); ok {
			roots = append(roots, root)
			continue
		}
		if sr.State == ServiceBlockedByDep {
			// No faulted ancestor is visible: the dependency wait itself is the
			// most specific fact this world can offer.
			roots = append(roots, RootCause{
				ID:         stableCauseID(sr.Name, DriftDependencyBlocked, lastExitReason(sr), lastExitRunID(sr)),
				Service:    sr.Name,
				Kind:       DriftDependencyBlocked,
				Severity:   SeverityHigh,
				Summary:    "blocked by a dependency that has not become ready",
				Evidence:   evidenceFor(sr, DriftDependencyBlocked),
				FirstError: sr.FirstError,
				Command:    "oberth status --json",
			})
		}
	}
	for i := range roots {
		roots[i].Impacted = impacted[roots[i].Service]
		sort.Strings(roots[i].Impacted)
	}
	sortRootCauses(roots)

	diagnosis := CausalDiagnosis{
		RootCauses: roots,
		Drifts:     aggregateDrifts(services),
		Loops:      cloneLoops(in.Loops),
	}
	diagnosis.NextActions = nextActions(diagnosis, services)
	return diagnosis
}

// dependencyExplains limits collapsing to the states a faulted dependency
// actually explains. A service that crashed on its own is still its own cause;
// a start in flight and a stopped service are waiting on the declaration, so a
// faulted ancestor explains them.
func dependencyExplains(sr ServiceRuntime, ancestors []string) bool {
	if len(ancestors) == 0 {
		return false
	}
	switch sr.State {
	case ServiceBlockedByDep, ServiceStarting, ServiceStopped:
		return true
	}
	return false
}

func faultedAncestors(name string, group state.Group, services []ServiceRuntime, index map[string]int) []string {
	var out []string
	path := map[string]bool{}
	var visit func(string)
	visit = func(current string) {
		svc := Service(group, current)
		if svc == nil {
			return
		}
		for _, dep := range svc.DependsOn {
			if path[dep] {
				continue
			}
			path[dep] = true
			i, known := index[dep]
			if !known {
				continue
			}
			if isFaulted(services[i]) {
				out = appendUnique(out, dep)
				continue
			}
			visit(dep)
		}
	}
	visit(name)
	return out
}

func isFaulted(sr ServiceRuntime) bool {
	switch sr.State {
	case ServiceCrashLoop, ServicePortConflict, ServiceCrashed, ServiceDegraded:
		return true
	}
	for _, drift := range sr.Drifts {
		if drift.Kind == DriftDependencyBlocked {
			continue
		}
		if drift.Severity == SeverityCritical || drift.Severity == SeverityHigh {
			return true
		}
	}
	return false
}

// rootCauseFor builds the one root cause a service carries, if it carries one.
func rootCauseFor(sr ServiceRuntime) (RootCause, bool) {
	drift, ok := faultDrift(sr)
	if !ok {
		return RootCause{}, false
	}
	command, _ := remediation(sr, drift)
	return RootCause{
		ID:         stableCauseID(sr.Name, drift.Kind, lastExitReason(sr), lastExitRunID(sr)),
		Service:    sr.Name,
		Kind:       drift.Kind,
		Severity:   drift.Severity,
		Summary:    drift.Summary,
		Evidence:   evidenceFor(sr, drift.Kind),
		FirstError: sr.FirstError,
		Command:    command,
	}, true
}

func faultDrift(sr ServiceRuntime) (Drift, bool) {
	// The lifecycle state and the root-cause kind have to agree. A service in
	// a crash loop whose latest ending happens to be a port conflict is still
	// a crash loop first: that is the repeated, generated fact.
	if sr.State == ServiceCrashLoop {
		for _, drift := range sr.Drifts {
			if drift.Kind == DriftRepeatedCrash {
				return drift, true
			}
		}
	}
	for _, drift := range sr.Drifts {
		switch drift.Kind {
		case DriftPortOccupied, DriftRepeatedCrash, DriftServiceCrashed, DriftStartFailed, DriftReadyTimeout:
			return drift, true
		}
	}
	for _, drift := range sr.Drifts {
		switch drift.Kind {
		case DriftHealthFailing, DriftUnownedListener:
			return drift, true
		}
	}
	return Drift{}, false
}

func remediation(sr ServiceRuntime, drift Drift) (command, safety string) {
	switch drift.Kind {
	case DriftPortOccupied:
		return portConflictRemediation(sr)
	case DriftUnownedListener:
		return "oberth status --json", SafetyHumanDecision
	case DriftCodeStale, DriftBinaryStale, DriftManifestStale:
		return "oberth restart --only " + sr.Name, SafetyLocalRestart
	default:
		if sr.LogPath == "" {
			return "oberth status --json", SafetyReadOnly
		}
		return "oberth logs " + sr.Name + " --once", SafetyReadOnly
	}
}

// portConflictRemediation never suggests signalling a process option-berth did
// not start. A previous generation of the same service is ours to stop; every
// other holder is a human decision.
func portConflictRemediation(sr ServiceRuntime) (command, safety string) {
	holder := sr.PortHolder
	if holder != nil && holder.Kind == state.PortHolderPrevious {
		return "oberth restart --only " + sr.Name, SafetyLocalRestart
	}
	return "oberth status --json", SafetyHumanDecision
}

func portConflictSummary(sr ServiceRuntime) string {
	port := "the declared port"
	if sr.LastExit != nil && sr.LastExit.PortHint > 0 {
		port = "port " + fmt.Sprint(sr.LastExit.PortHint)
	} else if sr.PortDeclared != nil {
		port = "port " + fmt.Sprint(*sr.PortDeclared)
	}
	holder := sr.PortHolder
	if holder == nil {
		return port + " was already occupied when the run tried to bind"
	}
	who := "pid " + fmt.Sprint(holder.PID)
	if holder.Command != "" {
		who += " (" + holder.Command + ")"
	}
	return port + " is held by " + who
}

func evidenceFor(sr ServiceRuntime, kind DriftKind) []string {
	var out []string
	if kind == DriftPortOccupied || kind == DriftUnownedListener {
		if holder := sr.PortHolder; holder != nil {
			out = append(out, "port_holder:pid="+fmt.Sprint(holder.PID))
			if holder.Kind != "" {
				out = append(out, "port_holder:kind="+string(holder.Kind))
			}
			if holder.Group != "" {
				out = append(out, "port_holder:group="+holder.Group)
			}
			if holder.Command != "" {
				out = append(out, "port_holder:command="+holder.Command)
			}
		}
	}
	if sr.LastExit != nil {
		out = append(out, "exit:"+sr.LastExit.Reason+":"+fmt.Sprint(sr.LastExit.Code))
	}
	if sr.LogPath != "" {
		out = append(out, "logs:"+sr.Name)
	}
	if sr.FirstError != "" {
		out = append(out, "first_error:"+sr.FirstError)
	}
	return out
}

func lastExitReason(sr ServiceRuntime) string {
	if sr.LastExit == nil {
		return ""
	}
	return sr.LastExit.Reason
}

func lastExitRunID(sr ServiceRuntime) string {
	if sr.LastExit == nil {
		return ""
	}
	return sr.LastExit.RunID
}

func stableCauseID(service string, kind DriftKind, reason, runID string) string {
	sum := sha256.Sum256([]byte(service + "|" + string(kind) + "|" + reason + "|" + runID))
	return hex.EncodeToString(sum[:8])
}

func sortRootCauses(roots []RootCause) {
	sort.SliceStable(roots, func(i, j int) bool {
		return severityRank(roots[i].Severity) < severityRank(roots[j].Severity)
	})
}

func severityRank(severity Severity) int {
	switch severity {
	case SeverityCritical:
		return 0
	case SeverityHigh:
		return 1
	case SeverityWarning:
		return 2
	default:
		return 3
	}
}

// aggregateDrifts carries the non-fatal drift rows to the diagnosis, naming
// the service on each so a reader can act without opening the service list.
func aggregateDrifts(services []ServiceRuntime) []Drift {
	var out []Drift
	seen := map[string]bool{}
	for _, sr := range services {
		for _, drift := range sr.Drifts {
			switch drift.Kind {
			case DriftCodeStale, DriftBinaryStale, DriftManifestStale:
			default:
				continue
			}
			row := drift
			row.Summary = sr.Name + ": " + drift.Summary
			key := string(row.Kind) + "|" + row.Summary + "|" + row.Command
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, row)
		}
	}
	return out
}

func nextActions(diagnosis CausalDiagnosis, services []ServiceRuntime) []NextAction {
	byName := make(map[string]ServiceRuntime, len(services))
	for _, sr := range services {
		byName[sr.Name] = sr
	}
	var actions []NextAction
	add := func(safety, service, summary, command string) {
		if command == "" {
			return
		}
		for _, existing := range actions {
			if existing.Command == command {
				return
			}
		}
		actions = append(actions, NextAction{Safety: safety, Service: service, Summary: summary, Command: command})
	}

	// Root-cause evidence and remediation come first.
	for _, root := range diagnosis.RootCauses {
		sr := byName[root.Service]
		command, safety := remediation(sr, Drift{Kind: root.Kind})
		if root.Command != "" {
			command = root.Command
		}
		add(safety, root.Service, "diagnose "+root.Service+": "+root.Summary, command)
		if safety == SafetyLocalRestart {
			add(SafetyLocalRestart, root.Service, "restart "+root.Service, "oberth restart --only "+root.Service)
		} else if root.Kind == DriftRepeatedCrash || root.Kind == DriftServiceCrashed || root.Kind == DriftStartFailed || root.Kind == DriftReadyTimeout || root.Kind == DriftHealthFailing {
			add(SafetyLocalRestart, root.Service, "restart "+root.Service+" once the cause is fixed",
				"oberth restart --only "+root.Service)
		}
	}
	// Then the declared restart for stale code, binary or manifest.
	for _, drift := range diagnosis.Drifts {
		service := restartServiceOf(drift.Command, services)
		add(SafetyLocalRestart, service, drift.Summary, drift.Command)
	}
	// Finally a loop with no root cause of its own (restart thrash).
	rooted := map[string]bool{}
	for _, root := range diagnosis.RootCauses {
		rooted[root.Service] = true
	}
	for _, loop := range diagnosis.Loops {
		if loop.Kind == LoopCrashLoop && rooted[loop.Service] {
			continue
		}
		add(SafetyReadOnly, loop.Service, loop.Summary, loop.Recommendation)
	}
	for i := range actions {
		actions[i].Priority = i + 1
	}
	return actions
}

// restartServiceOf recovers the service from the exact remediation command
// rather than parsing the summary, which would break on a service name that
// contains the separator.
func restartServiceOf(command string, services []ServiceRuntime) string {
	const prefix = "oberth restart --only "
	if !strings.HasPrefix(command, prefix) {
		return ""
	}
	name := strings.TrimPrefix(command, prefix)
	for _, sr := range services {
		if sr.Name == name {
			return name
		}
	}
	return name
}

func cloneLoops(loops []LoopSignal) []LoopSignal {
	if len(loops) == 0 {
		return nil
	}
	out := append([]LoopSignal(nil), loops...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func worldHealth(services []ServiceRuntime, diagnosis CausalDiagnosis, coverage string) WorldHealth {
	critical, high := false, false
	for _, root := range diagnosis.RootCauses {
		switch root.Severity {
		case SeverityCritical:
			critical = true
		case SeverityHigh:
			high = true
		}
	}
	if critical {
		return WorldBroken
	}
	if high {
		return WorldDegraded
	}
	if coverage == CoverageBlind || hasUnknownAbsence(services) {
		return WorldBlind
	}
	if anyState(services, ServiceStarting) {
		return WorldStarting
	}
	if len(diagnosis.Drifts) > 0 {
		return WorldDrifted
	}
	if len(services) > 0 && allState(services, ServiceReady) {
		return WorldHealthy
	}
	if !anyRunning(services) {
		return WorldIdle
	}
	return WorldDegraded
}

func diagnosisHealthy(health WorldHealth, diagnosis CausalDiagnosis, services []ServiceRuntime, coverage string) bool {
	if len(diagnosis.RootCauses) > 0 || len(diagnosis.Drifts) > 0 || len(diagnosis.Loops) > 0 {
		return false
	}
	if coverage == CoverageBlind || hasUnknownAbsence(services) {
		return false
	}
	return health == WorldHealthy || health == WorldIdle
}

func diagnosisSummary(health WorldHealth, diagnosis CausalDiagnosis, services []ServiceRuntime) string {
	switch {
	case len(diagnosis.RootCauses) > 0:
		impacted := 0
		for _, root := range diagnosis.RootCauses {
			impacted += len(root.Impacted)
		}
		return fmt.Sprintf("%d root cause(s), %d impacted service(s)", len(diagnosis.RootCauses), impacted)
	case len(diagnosis.Drifts) > 0:
		return fmt.Sprintf("%d non-fatal drift(s); a restart makes the run match the declaration", len(diagnosis.Drifts))
	case len(diagnosis.Loops) > 0:
		return fmt.Sprintf("%d restart loop signal(s)", len(diagnosis.Loops))
	case health == WorldBlind:
		return "listener observation is blind; absence from the table is not a fact"
	case health == WorldHealthy:
		return "all declared services are ready"
	case health == WorldIdle:
		return "no declared services are running"
	case health == WorldStarting:
		return "services are still starting"
	default:
		return fmt.Sprintf("%d of %d services are ready", countState(services, ServiceReady), len(services))
	}
}

func hasUnknownAbsence(services []ServiceRuntime) bool {
	for _, sr := range services {
		if sr.State == ServiceUnobserved && sr.LastExit == nil {
			return true
		}
	}
	return false
}

func anyState(services []ServiceRuntime, stateValue ServiceLifecycleState) bool {
	for _, sr := range services {
		if sr.State == stateValue {
			return true
		}
	}
	return false
}

func allState(services []ServiceRuntime, stateValue ServiceLifecycleState) bool {
	for _, sr := range services {
		if sr.State != stateValue {
			return false
		}
	}
	return true
}

func countState(services []ServiceRuntime, stateValue ServiceLifecycleState) int {
	n := 0
	for _, sr := range services {
		if sr.State == stateValue {
			n++
		}
	}
	return n
}

func anyRunning(services []ServiceRuntime) bool {
	for _, sr := range services {
		if sr.Running {
			return true
		}
	}
	return false
}

func loopFor(loops []LoopSignal, service string) *LoopSignal {
	var crash, thrash *LoopSignal
	for i := range loops {
		if loops[i].Service != service {
			continue
		}
		switch loops[i].Kind {
		case LoopCrashLoop:
			crash = &loops[i]
		case LoopRestartThrash:
			thrash = &loops[i]
		}
	}
	if crash != nil {
		return crash
	}
	return thrash
}

func latestExit(exits []ExitEvidence, service string) *ExitEvidence {
	var best *ExitEvidence
	var bestAt time.Time
	for i := range exits {
		if exits[i].Service != service {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, exits[i].At)
		if best == nil {
			best = &exits[i]
			bestAt = at
			continue
		}
		if err == nil && (bestAt.IsZero() || at.After(bestAt)) {
			best = &exits[i]
			bestAt = at
		}
	}
	return best
}

func firstErrorFor(in WorldInput, service string) string {
	if text := in.FirstErrors[service]; text != "" {
		return sanitizeFirstError(text)
	}
	for _, exit := range in.Exits {
		if exit.Service == service && exit.FirstError != "" {
			return sanitizeFirstError(exit.FirstError)
		}
	}
	return ""
}

func sanitizeFirstError(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	const limit = 240
	runes := []rune(text)
	if len(runes) > limit {
		text = string(runes[:limit])
	}
	return text
}

// ------------------------------------------------------------- revision ---

// revisionInput is the canonical, clock-free half of a world. Two reductions of
// the same intent, execution and observation facts hash to the same revision
// even when the read happened at a different moment.
type revisionInput struct {
	Worktree WorldIdentity     `json:"worktree"`
	Coverage string            `json:"coverage"`
	Services []revisionService `json:"services"`
	Machine  []revisionMachine `json:"machine,omitempty"`
	Ports    []revisionPort    `json:"ports,omitempty"`
	Exits    []revisionExit    `json:"exits,omitempty"`
	Loops    []revisionLoop    `json:"loops,omitempty"`
}

type revisionMachine struct {
	Name           string `json:"name"`
	Port           int    `json:"port"`
	Listening      bool   `json:"listening"`
	ListeningKnown bool   `json:"listening_known"`
	Unit           string `json:"unit,omitempty"`
}

type revisionService struct {
	Name              string   `json:"name"`
	Cmd               string   `json:"cmd,omitempty"`
	Cwd               string   `json:"cwd,omitempty"`
	PortDeclared      *int     `json:"port_declared,omitempty"`
	PortAuto          bool     `json:"port_auto,omitempty"`
	HealthDeclared    string   `json:"health_declared,omitempty"`
	DependsOn         []string `json:"depends_on,omitempty"`
	ReadyTimeout      string   `json:"ready_timeout,omitempty"`
	Running           bool     `json:"running"`
	RunID             string   `json:"run_id,omitempty"`
	PID               int      `json:"pid,omitempty"`
	CodeHead          string   `json:"code_head,omitempty"`
	SpecHash          string   `json:"spec_hash,omitempty"`
	PortActual        *int     `json:"port_actual,omitempty"`
	HealthStatus      string   `json:"health_status,omitempty"`
	HealthReason      string   `json:"health_reason,omitempty"`
	LastExitReason    string   `json:"last_exit_reason,omitempty"`
	LastExitCode      int      `json:"last_exit_code,omitempty"`
	LastExitRunID     string   `json:"last_exit_run_id,omitempty"`
	StartingPhase     string   `json:"starting_phase,omitempty"`
	StartingWaitingOn []string `json:"starting_waiting_on,omitempty"`
	ManifestMismatch  bool     `json:"manifest_mismatch,omitempty"`
	CodeChanged       *bool    `json:"code_changed,omitempty"`
	ExecutableChanged *bool    `json:"executable_changed,omitempty"`
}

type revisionPort struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	BindAddress  string `json:"bind_address,omitempty"`
	PID          int    `json:"pid,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	RunGroup     string `json:"run_group,omitempty"`
	RunName      string `json:"run_name,omitempty"`
	HealthStatus string `json:"health_status,omitempty"`
	HealthReason string `json:"health_reason,omitempty"`
}

type revisionExit struct {
	Service string `json:"service"`
	RunID   string `json:"run_id,omitempty"`
	Reason  string `json:"reason"`
	Code    int    `json:"code"`
}

type revisionLoop struct {
	Kind      LoopKind `json:"kind"`
	Service   string   `json:"service"`
	Count     int      `json:"count"`
	WindowSec int      `json:"window_sec"`
}

func revisionOf(in WorldInput) string {
	canonical := revisionInput{
		Worktree: identityOf(in.Group),
		Coverage: coverageOf(in.Observation),
	}
	for _, svc := range in.Group.Services {
		row := revisionService{
			Name:              svc.Name,
			Cmd:               svc.Cmd,
			Cwd:               svc.Cwd,
			PortDeclared:      cloneInt(svc.Port),
			PortAuto:          svc.PortAuto,
			DependsOn:         cloneStrings(svc.DependsOn),
			ReadyTimeout:      svc.ReadyTimeout,
			Running:           svc.Running,
			LastExitCode:      lastExitCode(svc.LastExit),
			ManifestMismatch:  svc.ManifestRuntimeMismatch,
			CodeChanged:       cloneBool(svc.CodeChangedSinceStart),
			ExecutableChanged: cloneBool(svc.ExecutableChangedSinceStart),
		}
		if svc.Health != nil {
			row.HealthDeclared = *svc.Health
		}
		if svc.RunID != nil {
			row.RunID = *svc.RunID
		}
		if svc.PID != nil {
			row.PID = *svc.PID
		}
		if svc.CodeHead != nil {
			row.CodeHead = *svc.CodeHead
		}
		if svc.RuntimeSpecHash != nil {
			row.SpecHash = *svc.RuntimeSpecHash
		}
		row.PortActual = cloneInt(svc.PortActual)
		if svc.HealthStatus != nil {
			row.HealthStatus = svc.HealthStatus.Status
			row.HealthReason = svc.HealthStatus.Reason
		}
		if svc.LastExit != nil {
			row.LastExitReason = svc.LastExit.Reason
			row.LastExitRunID = svc.LastExit.RunID
		}
		if svc.Starting != nil {
			row.StartingPhase = svc.Starting.Phase
			row.StartingWaitingOn = cloneStrings(svc.Starting.WaitingOn)
		}
		canonical.Services = append(canonical.Services, row)
	}
	for _, ref := range in.Group.Machine {
		row := revisionMachine{
			Name: ref.Name, Port: ref.Port, Listening: ref.Listening, ListeningKnown: ref.ListeningKnown,
		}
		if ref.Unit != nil {
			row.Unit = *ref.Unit
		}
		canonical.Machine = append(canonical.Machine, row)
	}
	for _, port := range in.Ports {
		if !portInScope(port, in.Group) {
			continue
		}
		row := revisionPort{
			Host: port.Host, Port: port.Port, BindAddress: port.BindAddress, PID: port.PID,
		}
		if port.Run != nil {
			row.RunID, row.RunGroup, row.RunName = port.Run.ID, port.Run.Group, port.Run.Name
		}
		if port.Health != nil {
			row.HealthStatus, row.HealthReason = port.Health.Status, port.Health.Reason
		}
		canonical.Ports = append(canonical.Ports, row)
	}
	sort.SliceStable(canonical.Ports, func(i, j int) bool {
		a, b := canonical.Ports[i], canonical.Ports[j]
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.BindAddress != b.BindAddress {
			return a.BindAddress < b.BindAddress
		}
		if a.PID != b.PID {
			return a.PID < b.PID
		}
		if a.RunID != b.RunID {
			return a.RunID < b.RunID
		}
		return a.RunGroup < b.RunGroup
	})
	seenExit := map[string]bool{}
	for _, exit := range in.Exits {
		key := fmt.Sprintf("%s|%s|%s|%d", exit.Service, exit.RunID, exit.Reason, exit.Code)
		if seenExit[key] {
			continue
		}
		seenExit[key] = true
		canonical.Exits = append(canonical.Exits, revisionExit{
			Service: exit.Service, RunID: exit.RunID, Reason: exit.Reason, Code: exit.Code,
		})
	}
	sort.SliceStable(canonical.Exits, func(i, j int) bool {
		a, b := canonical.Exits[i], canonical.Exits[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.RunID != b.RunID {
			return a.RunID < b.RunID
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.Code < b.Code
	})
	for _, loop := range in.Loops {
		canonical.Loops = append(canonical.Loops, revisionLoop{
			Kind: loop.Kind, Service: loop.Service, Count: loop.Count, WindowSec: loop.WindowSec,
		})
	}
	sort.SliceStable(canonical.Loops, func(i, j int) bool {
		if canonical.Loops[i].Service != canonical.Loops[j].Service {
			return canonical.Loops[i].Service < canonical.Loops[j].Service
		}
		return canonical.Loops[i].Kind < canonical.Loops[j].Kind
	})
	raw, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:12])
}

func portInScope(port state.Port, group state.Group) bool {
	if port.Group != nil && *port.Group == group.Name {
		return true
	}
	for _, svc := range group.Services {
		if svc.Port != nil && *svc.Port == port.Port {
			return true
		}
		if svc.PortActual != nil && *svc.PortActual == port.Port {
			return true
		}
	}
	return false
}

func lastExitCode(exit *state.ServiceExit) int {
	if exit == nil {
		return 0
	}
	return exit.Code
}

// ---------------------------------------------------------------- clones ---

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneHealth(health *state.Health) *state.Health {
	if health == nil {
		return nil
	}
	copied := *health
	return &copied
}

func cloneStarting(starting *state.ServiceStarting) *state.ServiceStarting {
	if starting == nil {
		return nil
	}
	copied := *starting
	copied.WaitingOn = cloneStrings(starting.WaitingOn)
	if starting.LastPrepareMS != nil {
		ms := *starting.LastPrepareMS
		copied.LastPrepareMS = &ms
	}
	return &copied
}

func cloneExit(exit *state.ServiceExit) *state.ServiceExit {
	if exit == nil {
		return nil
	}
	copied := *exit
	copied.PortHolder = cloneHolder(exit.PortHolder)
	return &copied
}

func cloneHolder(holder *state.PortHolder) *state.PortHolder {
	if holder == nil {
		return nil
	}
	copied := *holder
	return &copied
}

func cloneMachine(machine []state.MachineRef) []state.MachineRef {
	if machine == nil {
		return nil
	}
	out := make([]state.MachineRef, len(machine))
	for i, ref := range machine {
		out[i] = ref
		if ref.Unit != nil {
			unit := *ref.Unit
			out[i].Unit = &unit
		}
	}
	return out
}

// appendUnique keeps the first occurrence, preserving the caller's order.
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
