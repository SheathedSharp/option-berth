package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/docker"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/spf13/cobra"
)

var (
	// Selectors: positional ports or pids, or one of these filters.
	killPIDFlag     []int
	killGroupFlag   string
	killSessionFlag string
	killAllFlag     bool

	// How the stop is carried out.
	killTreeFlag  bool
	killForceFlag bool
	killGraceFlag time.Duration
	// Escalation and confirmation.
	killNoEscalateFlag bool
	killYesFlag        bool
	killDryRunFlag     bool
	killJSONFlag       bool
)

// killCmd is the one-stop command: port, pid, group or every listener.
var killCmd = &cobra.Command{
	Use:     "kill [port|pid ...]",
	Short:   "Stop a named listener or process",
	GroupID: commandGroupOperations,
	Long: `Stop a named listener. Docker-published ports are stopped with docker stop; a
listener option-berth started is stopped together with its whole process tree.

Examples:
  oberth kill 3000                      # SIGTERM the listener on port 3000
  oberth kill 3000 5432 --force         # SIGKILL both
  oberth kill --pid 12345 --tree        # a process and everything below it
  oberth kill --all -y                  # every listener, with confirmation
  oberth kill 3000 --ip 127.0.0.1       # disambiguate a multi-bind port
  oberth kill 3000 --tree --dry-run     # show the tree, change nothing

A positional argument is read as a port; a number no one is listening on that
matches a running process is read as a pid.`,
	RunE:              runKill,
	ValidArgsFunction: completePort,
}

func init() {
	flags := killCmd.Flags()
	flags.IntSliceVar(&killPIDFlag, "pid", nil, "Kill by process id (repeatable)")
	flags.StringVarP(&killGroupFlag, "group", "g", "", "Kill every port in a group")
	flags.StringVar(&killSessionFlag, "session", "", "Stop everything an agent `session` started (`current` is this shell's own)")
	flags.BoolVar(&killAllFlag, "all", false, "Kill every listening port")
	flags.BoolVar(&killTreeFlag, "tree", false, "Kill the target's whole process tree, children first")
	flags.BoolVarP(&killForceFlag, "force", "f", false, "Send SIGKILL instead of SIGTERM")
	flags.DurationVar(&killGraceFlag, "grace", killer.DefaultGrace, "How long to wait after SIGTERM before escalating (e.g. 10s)")
	flags.BoolVar(&killNoEscalateFlag, "no-escalate", false, "Never escalate to SIGKILL")
	flags.BoolVarP(&killYesFlag, "yes", "y", false, "Skip the confirmation prompt")
	flags.BoolVar(&killDryRunFlag, "dry-run", false, "Show what would be killed and do nothing")
	flags.String("ip", "", "Bind address, when a port is bound to several")
	flags.BoolVar(&killJSONFlag, "json", false, "Print the result list as JSON")

	// Group and agent-session selectors belonged to the old machine-wide run
	// registry surface. Keep them parseable for existing scripts, but make the
	// public escape hatch start with a named port or pid; project lifecycle is
	// `down`, and worktree state is `status`.
	for _, name := range []string{"group", "session"} {
		_ = flags.MarkHidden(name)
	}
	for _, pair := range [][2]string{{"group", "all"}, {"session", "all"}, {"session", "group"}} {
		killCmd.MarkFlagsMutuallyExclusive(pair[0], pair[1])
	}
	rootCmd.AddCommand(killCmd)
}

func runKill(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	bind, _ := cmd.Flags().GetString("ip")

	if killSessionFlag != "" {
		if len(args) != 0 || len(killPIDFlag) != 0 {
			return fmt.Errorf("--session cannot be combined with ports or pids")
		}
		return killSession(cmd.Context())
	}

	// A reachable daemon does the killing, so it rescans straight afterwards
	// and the history ring sees the port go down. Without this
	// the daemon's own cache could still show the port for up to CacheTTL after
	// `oberth kill` returned. The connect is dial-only, like the read commands
	// in §20: killing is not a reason to start a daemon.
	if c := daemonClient(cmd.Context()); c != nil {
		defer c.Close()
		return killThroughDaemon(cmd.Context(), c, args, bind)
	}
	return killDirect(cmd.Context(), args, bind)
}

// killDirect is the no-daemon path: scan here, kill here. It is also what runs
// under --no-daemon and whenever the daemon is down, which is why it stays a
// complete implementation rather than a degraded one.
func killDirect(ctx context.Context, args []string, bindIP string) error {
	rows, targets, confirm, complete, err := killSelection(ctx, args, bindIP, scanForKill)
	if err != nil {
		return err
	}

	if len(targets) == 0 {
		return reportKill(os.Stdout, nil, rows, killJSONFlag, killDryRunFlag)
	}

	opts := killOptions()
	// Only a complete observation may stand in for the killer's own scan. A
	// partial table is attribution for the report; publishing it here would
	// suppress the killer's re-observation and let an unreadable row become a
	// signal.
	if complete {
		opts.Ports = rows
	}

	return killRequest{
		exec:     killHere,
		targets:  targets,
		snapshot: rows,
		opts:     opts,
		confirm:  confirm && !killYesFlag && !killDryRunFlag,
		asJSON:   killJSONFlag,
	}.execute(ctx)
}

// killSelection resolves the command line against the enriched listener table.
// The table is read for every selector, not only for the ones that pick their
// targets out of it: it is what carries a listener's run ownership, and that
// ownership decides how much of a run `--pid` stops, what the result rows are
// called, and which addresses a report says were released. Reading it only for
// the table-driven selectors made `kill --pid <a run's listener>` stop a leaf
// instead of its run and name the process instead of the service.
//
// A failed read never becomes an empty table. For the selectors that resolve
// through it (`--all`, `--group`, a positional argument) the observation failure
// is the answer: selecting nothing from a blind table and reporting "Nothing to
// stop." would turn "cannot see" into "nothing is running", and `kill --all`
// would exit 0 without having observed a single listener.
//
// An explicit `--pid` is the exception. The pid is the whole subject, so a
// failed observation must not deny it — but that exception is about
// *selection*, not about stopping without evidence: the killer observes again
// before it signals, so a blind machine still ends in a failed row rather than
// a silent success. A partial read keeps the rows it did produce, because those
// rows are exactly the attribution this function exists to preserve.
func killSelection(ctx context.Context, args []string, bindIP string, scan func(context.Context) ([]ports.ListeningPort, error)) ([]ports.ListeningPort, []killer.Target, bool, bool, error) {
	snapshot, err := scan(ctx)
	complete := err == nil
	if err != nil {
		if killRequiresObservation(args) {
			return nil, nil, false, false, err
		}
		snapshot, _ = ports.PartialRows(err)
	}
	targets, confirm, err := killTargets(args, snapshot, bindIP)
	return snapshot, targets, confirm, complete, err
}

// killRequiresObservation reports whether a failed observation vetoes this
// selection: positional arguments are read port-first, and the group and all
// selectors are sweeps. Only --pid alone survives a table it could not read.
func killRequiresObservation(args []string) bool {
	return len(args) > 0 || killGroupFlag != "" || killAllFlag
}

// killOptions is the killer configuration the flags describe, shared by both
// paths so the daemon is asked for exactly what the direct path would do.
func killOptions() killer.Options {
	opts := killer.Options{
		DryRun: killDryRunFlag,
		Tree:   killTreeFlag,
		Force:  killForceFlag,
		Grace:  killGraceFlag,
	}
	if killNoEscalateFlag {
		neverEscalate := false
		opts.Escalate = &neverEscalate
	}
	return opts
}

// killExecutor performs one round against the selected targets. dryRun asks
// only for the plan, which is what the confirmation shows before the real
// round; the caller decides where signalling happens (this process or the
// daemon), while every prompt and report stays above this seam.
type killExecutor func(ctx context.Context, targets []killer.Target, opts killer.Options, dryRun bool) ([]killer.Result, error)

// killHere is the executor that signals from this process. Production points
// it at signalListenersForKill, the only command implementation that may call
// killer.KillPorts. Tests replace the hook; a missed replacement must fail
// closed rather than signal an unowned process.
var killHere killExecutor = signalListenersForKill

func signalListenersForKill(ctx context.Context, targets []killer.Target, opts killer.Options, dryRun bool) ([]killer.Result, error) {
	round := opts
	round.DryRun = dryRun
	return killer.KillPorts(ctx, targets, round), nil
}

// killRequest is one kill operation after its targets have been chosen:
// optionally confirm the plan, run it once, report the result. The port and
// group forms, the hidden session form and the daemon and direct routes all
// differ in how targets are selected or where the signal is sent, never in
// this decision order.
type killRequest struct {
	exec     killExecutor
	targets  []killer.Target
	snapshot []ports.ListeningPort
	opts     killer.Options
	confirm  bool
	asJSON   bool
	// emptyPlan, when set, is the whole answer for a confirmed plan that
	// selected nothing (a named session with no members). The port forms
	// handle an empty selection before reaching here.
	emptyPlan func()
}

func (r killRequest) execute(ctx context.Context) error {
	// The confirmation lists the plan the executor actually produced, so the
	// prompt shows exactly what will happen — including the whole tree.
	if r.confirm {
		plan, err := r.exec(ctx, r.targets, r.opts, true)
		if err != nil {
			return cliError(err)
		}
		if len(plan) == 0 && r.emptyPlan != nil {
			r.emptyPlan()
			return nil
		}
		if !confirmPlan(plan, r.snapshot) {
			fmt.Println("Aborted.")
			return nil
		}
	}
	results, err := r.exec(ctx, r.targets, r.opts, r.opts.DryRun)
	if err != nil {
		return cliError(err)
	}
	return reportKill(os.Stdout, results, r.snapshot, r.asJSON, r.opts.DryRun)
}

// scanForKill is the observation seam the direct path resolves every selector
// against, and which the killer reuses instead of scanning a second time.
// Production points it at observeListenersForKill, the only command
// implementation that may call ports.Scan. Tests replace the hook; a missed
// replacement must fail closed rather than observe the machine.
var scanForKill = observeListenersForKill

// observeListenersForKill takes the enriched scan group attribution is part
// of: without it `-g` would only ever see a Compose project or a run tag,
// never a `oberth.yaml` name or a git root. It takes the command context so a
// slow collector can be interrupted; the killer's own re-observation keeps its
// separate request budget.
func observeListenersForKill(ctx context.Context) ([]ports.ListeningPort, error) {
	found, err := ports.ScanContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := docker.EnrichPortsContext(ctx, found); err != nil {
		return nil, err
	}
	if err := ports.EnrichContext(ctx, found); err != nil {
		return nil, err
	}
	groups.Attribute(found)
	return found, ctx.Err()
}

// killTargets turns the command line into killer targets. The bool reports
// whether the selection is broad enough to confirm before acting.
func killTargets(args []string, snapshot []ports.ListeningPort, bindIP string) ([]killer.Target, bool, error) {
	targets := make([]killer.Target, 0, len(killPIDFlag)+len(args))
	for _, pid := range killPIDFlag {
		targets = append(targets, killer.Target{PID: pid})
	}
	for _, arg := range args {
		number, convErr := strconv.Atoi(arg)
		if convErr != nil || number <= 0 {
			return nil, false, fmt.Errorf("invalid port or pid: %s", arg)
		}
		targets = append(targets, positionalTarget(number, snapshot, bindIP))
	}

	if killGroupFlag != "" {
		if len(targets) != 0 {
			return nil, false, fmt.Errorf("--group cannot be combined with ports or pids")
		}
		if members := groupMembers(snapshot, killGroupFlag); len(members) != 0 {
			return members, true, nil
		}
		return nil, false, fmt.Errorf("no listening port belongs to group %q", killGroupFlag)
	}
	if killAllFlag {
		if len(targets) != 0 {
			return nil, false, fmt.Errorf("--all cannot be combined with ports or pids")
		}
		// An empty sweep is not a failure: there was simply nothing to stop.
		return sweepTargets(snapshot), true, nil
	}

	if len(targets) == 0 {
		return nil, false, fmt.Errorf("nothing to kill: give a port or pid, or use --pid, --group or --all")
	}
	return targets, false, nil
}

// positionalTarget treats a bare number as a port, unless nothing listens on
// it while a process with that id exists.
func positionalTarget(n int, snapshot []ports.ListeningPort, bindIP string) killer.Target {
	return positionalTargetWithProbe(n, snapshot, bindIP, killer.Alive)
}

func positionalTargetWithProbe(n int, snapshot []ports.ListeningPort, bindIP string, alive func(int) bool) killer.Target {
	for _, p := range snapshot {
		if p.Port == n {
			return killer.Target{Port: n, BindAddress: bindIP}
		}
	}
	if n > 65535 || alive(n) {
		return killer.Target{PID: n}
	}
	return killer.Target{Port: n, BindAddress: bindIP}
}

// groupMembers resolves a group name against the scan. The scanner's `group`
// field is the primary signal; an `oberth run` tag or id and a Docker Compose
// project name are accepted too, so the flag reaches everything grouped today.
func groupMembers(snapshot []ports.ListeningPort, name string) []killer.Target {
	targets := make([]killer.Target, 0, len(snapshot))
	for _, row := range snapshot {
		if inGroup(row, name) {
			targets = append(targets, killer.Target{Port: row.Port, BindAddress: row.BindAddress})
		}
	}
	return targets
}

func inGroup(p ports.ListeningPort, name string) bool {
	for _, field := range []string{p.Group, p.Tag, p.RunID, p.DockerComposeProject} {
		if field != "" && strings.EqualFold(field, name) {
			return true
		}
	}
	return false
}

// sweepTargets is the explicit all-listener exception. It intentionally has
// no machine-wide classification or project narrowing: those selectors belong
// to the worktree-scoped status view, while kill remains a named operation.
func sweepTargets(snapshot []ports.ListeningPort) []killer.Target {
	var out []killer.Target
	for _, p := range snapshot {
		out = append(out, killer.Target{Port: p.Port, BindAddress: p.BindAddress})
	}
	return out
}

// confirmPlan prints the planned actions and asks before taking them.
func confirmPlan(plan []killer.Result, snapshot []ports.ListeningPort) bool {
	fmt.Printf("Will stop %d process(es):\n", len(plan))
	for i := range plan {
		fmt.Printf("  - %s\n", describeResult(plan[i], snapshot))
	}
	fmt.Print("\nProceed? [y/N] ")
	var answer string
	fmt.Scanln(&answer)
	answer = strings.TrimSpace(answer)
	return strings.EqualFold(answer, "y")
}

// reportKill prints the outcome. Any target that failed (a missing one does
// not count) makes the returned error non-nil, so the command exits 1.
//
// **A target that was already gone is not a failure.** The end state the caller
// asked for — that thing is not running — already holds, so its row is reported
// as a fact and counted as stopped rather than failed. Without this, `down` on
// a project whose last process had already exited printed `2/3 stopped` and
// `1 of 3 targets failed` while everything really was stopped.
func reportKill(w io.Writer, results []killer.Result, snapshot []ports.ListeningPort, asJSON, dryRun bool) error {
	failed := 0
	for i := range results {
		if !results[i].OK && !alreadyGone(results[i]) {
			failed++
		}
	}

	if asJSON {
		if err := writeKillJSON(w, results); err != nil {
			return err
		}
		if failed != 0 {
			return errSilent
		}
		return nil
	}

	// Nothing to report is still a successful answer.
	if len(results) == 0 {
		fmt.Fprintln(w, "Nothing to stop.")
		return nil
	}
	// A dry run says up front that nothing below was carried out.
	if dryRun {
		fmt.Fprintf(w, "Dry run: %d action(s), children first.\n", len(results))
	}
	for _, r := range results {
		switch {
		case r.OK:
			fmt.Fprintf(w, "%s %s\n", killVerb(r, dryRun), describeResult(r, snapshot))
		case alreadyGone(r):
			fmt.Fprintf(w, "%s — nothing to stop: %s\n", describeResult(r, snapshot), r.Error)
		default:
			fmt.Fprintf(w, "error: %s\n", r.Error)
		}
	}
	if !dryRun {
		for _, url := range freedURLs(results, snapshot) {
			fmt.Fprintf(w, "released %s\n", display.Underline(url))
		}
		fmt.Fprintf(w, "\n%d/%d stopped.\n", len(results)-failed, len(results))
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d targets failed", failed, len(results))
	}
	return nil
}

// alreadyGone reports whether a row's target was not there to stop. The verdict
// reads the row's code, not its error prose.
func alreadyGone(r killer.Result) bool { return r.Code == killer.CodeNotFound }

// killVerb is the leading word of a result line: what was done in the
// reader's vocabulary, or — in a dry run — what would be. The signal name is
// the wire's word for it; on screen the verb is "stop"/"stopped", with the
// method in parentheses only when the caller explicitly asked for the
// harsher one.
func killVerb(r killer.Result, dryRun bool) string {
	switch r.Method {
	case state.MethodDockerStop:
		if dryRun {
			return "would stop container"
		}
		return "stopped container"
	case state.MethodSIGKILL:
		if dryRun {
			return "would stop (SIGKILL)"
		}
		return "stopped (SIGKILL)"
	case state.MethodSIGTERM:
		if dryRun {
			return "would stop (SIGTERM)"
		}
		return "stopped (SIGTERM)"
	case state.MethodNone:
		if dryRun {
			return "would do nothing"
		}
		return "nothing done"
	default:
		if dryRun {
			return "would stop"
		}
		return "stopped"
	}
}

// describeResult names the process or container a row acted on.
func describeResult(r killer.Result, snapshot []ports.ListeningPort) string {
	label := r.Name
	if label == "" {
		label = "unknown"
	}
	var out strings.Builder
	out.WriteString(display.Bold(label))
	if r.PID > 0 {
		fmt.Fprintf(&out, " (PID %d)", r.PID)
	}
	if r.Port > 0 {
		fmt.Fprintf(&out, " on port %d", r.Port)
		if bindAmbiguous(snapshot, r.Port) && r.BindAddress != "" {
			fmt.Fprintf(&out, " [%s]", r.BindAddress)
		}
	}
	return out.String()
}

// bindAmbiguous reports whether a port number appears more than once in the
// scan, in which case the bind address is worth printing.
func bindAmbiguous(snapshot []ports.ListeningPort, port int) bool {
	matches := 0
	for _, row := range snapshot {
		if row.Port == port {
			matches++
		}
	}
	return matches > 1
}

// freedURLs lists the URLs the successful rows released: distinct, in the order
// the results report them.
func freedURLs(results []killer.Result, snapshot []ports.ListeningPort) []string {
	byKey := map[string]string{}
	for i := range snapshot {
		byKey[snapshot[i].PortKey()] = snapshot[i].URL()
	}
	var urls []string
	emitted := map[string]bool{}
	for _, row := range results {
		if !row.OK || row.Port == 0 {
			continue
		}
		key := row.Key()
		if emitted[key] {
			continue
		}
		emitted[key] = true
		if url, found := byKey[key]; found {
			urls = append(urls, url)
		}
	}
	return urls
}

// writeKillJSON prints the kill result list.
func writeKillJSON(w io.Writer, rows []killer.Result) error {
	if rows == nil {
		rows = []killer.Result{}
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(rows)
}
