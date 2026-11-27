package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/spf13/cobra"
)

var runtimeInspectJSONFlag bool

var runtimeInspectCmd = &cobra.Command{
	Use:   "inspect [path]",
	Short: "One worktree's runtime world: services, drifts, root causes and next actions",
	Long: "Read the Runtime World for a worktree: each declared service's lifecycle state,\n" +
		"the drift between the declaration and the run, and the causal diagnosis that\n" +
		"collapses a dependency cascade into the failure that started it.\n\n" +
		"The world is reduced from the facts `status` already collects, so it works with\n" +
		"or without a running Runtime Service. With the service up, its loop signals are\n" +
		"joined in; with it down, the document is still a complete local read.\n\n" +
		"With --json the Runtime World is printed as one document. The human view\n" +
		"orders the same facts as a health verdict, a service table, root causes with the\n" +
		"services they impact, and the next actions in priority order.",
	Args: cobra.MaximumNArgs(1),
	RunE: runRuntimeInspect,
}

func init() {
	runtimeInspectCmd.Flags().BoolVar(&runtimeInspectJSONFlag, "json", false, "Output as JSON")
	runtimeCmd.AddCommand(runtimeInspectCmd)
}

func runRuntimeInspect(cmd *cobra.Command, args []string) error {
	dir, err := statusDir(args)
	if err != nil {
		return err
	}
	world, err := runtimeInspectWorld(cmd.Context(), dir)
	if err != nil {
		return err
	}
	if runtimeInspectJSONFlag {
		return printJSON(world)
	}
	printRuntimeInspect(world)
	return nil
}

// runtimeInspectWorld is the read behind the command: the status document's
// facts, bounded first errors, and the Runtime Service's loop signals when it
// has the capability. The reduction itself is pure.
func runtimeInspectWorld(ctx context.Context, dir string) (runtimecore.World, error) {
	doc, err := buildStatusDocument(ctx, dir, false, nil)
	if err != nil {
		return runtimecore.World{}, err
	}
	firstErrors := runtimeInspectFirstErrors(doc)
	return runtimecore.ReduceWorld(runtimecore.WorldInput{
		ObservedAt:  time.Now().UTC().Format(time.RFC3339),
		Group:       doc.Worktree,
		Ports:       doc.Ports,
		Observation: doc.Observation,
		Exits:       inspectExitEvidence(doc),
		FirstErrors: firstErrors,
		Loops:       runtimeInspectLoops(ctx, doc.Worktree.Name, doc.Scope.Root),
	}), nil
}

// runtimeInspectLoops joins the Runtime Service's loop window when that service
// is up and announces the capability. A missing service, an older service or a
// failed call all mean the same thing here: no loop evidence, not an error.
func runtimeInspectLoops(ctx context.Context, group, root string) []runtimecore.LoopSignal {
	client := dialOrNil(ctx)
	if client == nil {
		return nil
	}
	defer client.Close()
	if !slices.Contains(client.Hello().Capabilities, rpc.CapabilityRuntimeWorld) {
		return nil
	}
	var result rpc.RuntimeWorldReadResult
	if err := client.Call(ctx, "runtime.world.read", rpc.RuntimeWorldReadParams{
		Group: group, WorktreePath: root,
	}, &result); err != nil {
		return nil
	}
	return result.World.Diagnosis.Loops
}

// runtimeInspectFirstErrors reads the first error line of the newest failed run
// for each service. The read is bounded by the run's own log offset and one
// fixed byte budget, falling back to the run's bounded exit tail lines when the
// log file is missing, rotated or printed past the budget before failing.
func runtimeInspectFirstErrors(doc statusDocument) map[string]string {
	out := map[string]string{}
	seen := map[string]bool{}
	// A service's current last_exit names the generation whose error is still
	// relevant. An older failed run is history, even when it is in the bounded
	// exit list; reading its first error would attach stale evidence to a
	// service that is now running.
	current := map[string]string{}
	for _, service := range doc.Worktree.Services {
		if service.LastExit != nil {
			current[service.Name] = service.LastExit.RunID
		}
	}
	for _, run := range doc.Exits {
		if run.Name == "" || seen[run.Name] || !isFailureReason(run.Reason) {
			continue
		}
		if want, known := current[run.Name]; known && want != "" && run.ID != want {
			continue
		}
		seen[run.Name] = true
		offset := int64(0)
		if run.LogOffset != nil {
			offset = *run.LogOffset
		}
		if text := runtimecore.FirstErrorFromExit(run.LogPath, offset, run.LastLines); text != "" {
			out[run.Name] = text
		}
	}
	return out
}

func inspectExitEvidence(doc statusDocument) []runtimecore.ExitEvidence {
	out := make([]runtimecore.ExitEvidence, 0, len(doc.Exits))
	for _, run := range doc.Exits {
		code := 0
		if run.ExitCode != nil {
			code = *run.ExitCode
		}
		out = append(out, runtimecore.ExitEvidence{
			Service: run.Name, RunID: run.ID, Reason: run.Reason, Code: code,
			At: run.ExitedAt, LogPath: run.LogPath,
		})
	}
	return out
}

// printRuntimeInspect is the human view: the same facts the JSON document
// carries, ordered from the worktree verdict down to the first action.
func printRuntimeInspect(world runtimecore.World) {
	head := display.Bold(world.Worktree.Group)
	if world.Worktree.Root != "" {
		head += "  " + display.Dim(shortPath(world.Worktree.Root))
	}
	if world.Worktree.Branch != "" {
		head += "  " + display.Dim(world.Worktree.Branch)
	}
	fmt.Println(head)
	fmt.Printf("  %s %s\n", display.PadDisplay(display.Dim("health"), 10), healthText(world.Health))
	if world.Coverage != runtimecore.CoverageObservable {
		fmt.Printf("  %s %s\n", display.PadDisplay(display.Dim("coverage"), 10), display.Yellow(world.Coverage))
	}
	if world.Diagnosis.Summary != "" {
		fmt.Printf("  %s %s\n", display.PadDisplay(display.Dim("summary"), 10), world.Diagnosis.Summary)
	}

	printRuntimeInspectServices(world)
	printRuntimeInspectRootCauses(world)
	printRuntimeInspectLoops(world)
	printRuntimeInspectActions(world)
}

func printRuntimeInspectServices(world runtimecore.World) {
	if len(world.Services) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("  %s %s %s %s %s\n",
		display.PadDisplay(display.Dim("service"), 16),
		display.PadDisplay(display.Dim("state"), 14),
		display.PadDisplay(display.Dim("port"), 12),
		display.PadDisplay(display.Dim("health"), 10),
		display.Dim("drift"))
	for _, service := range world.Services {
		drifts := make([]string, 0, len(service.Drifts))
		for _, drift := range service.Drifts {
			drifts = append(drifts, string(drift.Kind))
		}
		driftText := "-"
		if len(drifts) > 0 {
			driftText = strings.Join(drifts, ", ")
		}
		fmt.Printf("  %s %s %s %s %s\n",
			display.PadDisplay(service.Name, 16),
			display.PadDisplay(lifecycleText(service.State), 14),
			display.PadDisplay(portCell(service), 12),
			display.PadDisplay(healthCell(service), 10),
			driftText)
	}
}

func printRuntimeInspectRootCauses(world runtimecore.World) {
	if len(world.Diagnosis.RootCauses) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("  %s\n", display.Bold("root causes"))
	for _, root := range world.Diagnosis.RootCauses {
		fmt.Printf("  %s · %s · %s\n", display.Red(root.Service), root.Kind, root.Summary)
		if len(root.Impacted) > 0 {
			fmt.Printf("    impacted: %s\n", strings.Join(root.Impacted, ", "))
		}
		if root.FirstError != "" {
			fmt.Printf("    first error: %s\n", root.FirstError)
		}
		if root.Command != "" {
			fmt.Printf("    next: %s\n", display.Dim(root.Command))
		}
	}
}

func printRuntimeInspectLoops(world runtimecore.World) {
	if len(world.Diagnosis.Loops) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("  %s\n", display.Bold("loops"))
	for _, loop := range world.Diagnosis.Loops {
		fmt.Printf("  %s · %s · %s\n", loop.Service, loop.Kind, loop.Summary)
	}
}

func printRuntimeInspectActions(world runtimecore.World) {
	if len(world.Diagnosis.NextActions) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("  %s\n", display.Bold("next"))
	for _, action := range world.Diagnosis.NextActions {
		fmt.Printf("  %d. [%s] %s\n", action.Priority, action.Safety, action.Command)
	}
}

func lifecycleText(state runtimecore.ServiceLifecycleState) string {
	switch state {
	case runtimecore.ServiceReady:
		return display.Green(string(state))
	case runtimecore.ServiceCrashed, runtimecore.ServiceCrashLoop, runtimecore.ServicePortConflict,
		runtimecore.ServiceDegraded:
		return display.Red(string(state))
	case runtimecore.ServiceStarting, runtimecore.ServiceBlockedByDep:
		return display.Yellow(string(state))
	default:
		return display.Dim(string(state))
	}
}

func portCell(service runtimecore.ServiceRuntime) string {
	if service.PortActual != nil {
		return fmt.Sprint(*service.PortActual)
	}
	if service.PortDeclared != nil {
		return "declared " + fmt.Sprint(*service.PortDeclared)
	}
	if service.PortAuto {
		return "auto"
	}
	return "-"
}

func healthCell(service runtimecore.ServiceRuntime) string {
	if service.Health == nil || service.Health.Status == "" {
		return "-"
	}
	switch service.Health.Status {
	case "ok":
		return display.Green(service.Health.Status)
	case "fail":
		return display.Red(service.Health.Status)
	default:
		return display.Yellow(service.Health.Status)
	}
}

func healthText(health runtimecore.WorldHealth) string {
	switch health {
	case runtimecore.WorldHealthy:
		return display.Green(string(health))
	case runtimecore.WorldBroken:
		return display.Red(string(health))
	case runtimecore.WorldDegraded, runtimecore.WorldDrifted, runtimecore.WorldBlind:
		return display.Yellow(string(health))
	default:
		return display.Dim(string(health))
	}
}
