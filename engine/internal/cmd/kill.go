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

	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/docker"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/killer"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/spf13/cobra"
)

var (
	killPIDFlag        []int
	killGroupFlag      string
	killSessionFlag    string
	killAllFlag        bool
	killTreeFlag       bool
	forceFlag          bool
	killGraceFlag      time.Duration
	killNoEscalateFlag bool
	killYesFlag        bool
	killDryRunFlag     bool
	killJSONFlag       bool
)

var killCmd = &cobra.Command{
	Use:     "kill [port|pid ...]",
	Short:   "Stop a named listener or process",
	GroupID: commandGroupSupport,
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
	ValidArgsFunction: completePort,
	RunE:              runKill,
}

func init() {
	killCmd.Flags().IntSliceVar(&killPIDFlag, "pid", nil, "Kill by process id (repeatable)")
	killCmd.Flags().StringVarP(&killGroupFlag, "group", "g", "", "Kill every port in a group")
	killCmd.Flags().StringVar(&killSessionFlag, "session", "",
		"Stop everything an agent `session` started (`current` is this shell's own)")
	killCmd.Flags().BoolVar(&killAllFlag, "all", false, "Kill every listening port")
	killCmd.Flags().BoolVar(&killTreeFlag, "tree", false, "Kill the target's whole process tree, children first")
	killCmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "Send SIGKILL instead of SIGTERM")
	killCmd.Flags().DurationVar(&killGraceFlag, "grace", killer.DefaultGrace,
		"How long to wait after SIGTERM before escalating (e.g. 10s)")
	killCmd.Flags().BoolVar(&killNoEscalateFlag, "no-escalate", false, "Never escalate to SIGKILL")
	killCmd.Flags().BoolVarP(&killYesFlag, "yes", "y", false, "Skip the confirmation prompt")
	killCmd.Flags().BoolVar(&killDryRunFlag, "dry-run", false, "Show what would be killed and do nothing")
	killCmd.Flags().String("ip", "", "Bind address, when a port is bound to several")
	killCmd.Flags().BoolVar(&killJSONFlag, "json", false, "Print the result list as JSON")
	// Group and agent-session selectors belonged to the old machine-wide run
	// registry surface. Keep them parseable for existing scripts, but make the
	// public escape hatch start with a named port or pid; project lifecycle is
	// `down`, and worktree state is `status`.
	for _, name := range []string{"group", "session"} {
		_ = killCmd.Flags().MarkHidden(name)
	}
	killCmd.MarkFlagsMutuallyExclusive("group", "all")
	killCmd.MarkFlagsMutuallyExclusive("session", "all")
	killCmd.MarkFlagsMutuallyExclusive("session", "group")
	rootCmd.AddCommand(killCmd)
}

func runKill(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	bindIP, _ := cmd.Flags().GetString("ip")

	if killSessionFlag != "" {
		if len(args) > 0 || len(killPIDFlag) > 0 {
			return fmt.Errorf("--session cannot be combined with ports or pids")
		}
		return killSession(cmd.Context())
	}

	// A reachable daemon does the killing, so it rescans straight afterwards
	// and the history ring sees the port go down (contract §22). Without this
	// the daemon's own cache could still show the port for up to CacheTTL after
	// `oberth kill` returned. The connect is dial-only, like the read commands
	// in §20: killing is not a reason to start a daemon.
	if c := daemonClient(cmd.Context()); c != nil {
		defer c.Close()
		return killThroughDaemon(cmd.Context(), c, args, bindIP)
	}
	return killDirect(cmd.Context(), args, bindIP)
}

// killDirect is the no-daemon path: scan here, kill here. It is also what runs
// under --no-daemon and whenever the daemon is down, which is why it stays a
// complete implementation rather than a degraded one.
func killDirect(ctx context.Context, args []string, bindIP string) error {
	snapshot := scanForKill()
	targets, confirm, err := killTargets(args, snapshot, bindIP)
	if err != nil {
		return err
	}

	if len(targets) == 0 {
		return reportKill(os.Stdout, nil, snapshot, killJSONFlag, killDryRunFlag)
	}

	opts := killOptions()
	opts.Ports = snapshot

	return killRun(ctx, targets, snapshot, opts,
		confirm && !killYesFlag && !killDryRunFlag, killJSONFlag)
}

// killOptions is the killer configuration the flags describe, shared by both
// paths so the daemon is asked for exactly what the direct path would do.
func killOptions() killer.Options {
	opts := killer.Options{
		Tree:   killTreeFlag,
		Force:  forceFlag,
		Grace:  killGraceFlag,
		DryRun: killDryRunFlag,
	}
	if killNoEscalateFlag {
		off := false
		opts.Escalate = &off
	}
	return opts
}

// killRun is the body every kill-shaped command shares: confirm the plan when
// asked to, run it through the killer, print the outcome. `kill-all` and
// `down` are nothing but different ways of choosing targets for it.
func killRun(ctx context.Context, targets []killer.Target, snapshot []ports.ListeningPort,
	opts killer.Options, confirm, asJSON bool) error {
	// The confirmation lists the plan the killer actually produced, so the
	// prompt shows exactly what will happen — including the whole tree.
	if confirm {
		plan := opts
		plan.DryRun = true
		if !confirmPlan(killer.KillPorts(ctx, targets, plan), snapshot) {
			fmt.Println("Aborted.")
			return nil
		}
	}
	results := killer.KillPorts(ctx, targets, opts)
	return reportKill(os.Stdout, results, snapshot, asJSON, opts.DryRun)
}

// scanForKill takes the enriched scan every selector is resolved against, and
// which the killer reuses instead of scanning a second time. Group attribution
// is part of that enrichment: without it `-g` would only ever see a Compose
// project or a run tag, never a `oberth.yaml` name or a git root.
func scanForKill() []ports.ListeningPort {
	found, err := ports.Scan()
	if err != nil {
		return nil
	}
	docker.EnrichPorts(found)
	ports.Enrich(found)
	groups.Attribute(found)
	return found
}

// killTargets turns the command line into killer targets. The bool reports
// whether the selection is broad enough to confirm before acting.
func killTargets(args []string, snapshot []ports.ListeningPort, bindIP string) ([]killer.Target, bool, error) {
	var targets []killer.Target
	for _, pid := range killPIDFlag {
		targets = append(targets, killer.Target{PID: pid})
	}
	for _, arg := range args {
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
			return nil, false, fmt.Errorf("invalid port or pid: %s", arg)
		}
		targets = append(targets, positionalTarget(n, snapshot, bindIP))
	}

	switch {
	case killGroupFlag != "":
		if len(targets) > 0 {
			return nil, false, fmt.Errorf("--group cannot be combined with ports or pids")
		}
		members := groupMembers(snapshot, killGroupFlag)
		if len(members) == 0 {
			return nil, false, fmt.Errorf("no listening port belongs to group %q", killGroupFlag)
		}
		return members, true, nil

	case killAllFlag:
		if len(targets) > 0 {
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

// positionalTarget reads a bare number as a port, falling back to a pid when
// nothing is listening on it but a process with that id exists.
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
// field is the primary signal; a `option-berth run` tag or id and a Docker Compose
// project name are accepted too, so the flag works for everything that is
// grouped today.
func groupMembers(snapshot []ports.ListeningPort, name string) []killer.Target {
	var out []killer.Target
	for _, p := range snapshot {
		if inGroup(p, name) {
			out = append(out, killer.Target{Port: p.Port, BindAddress: p.BindAddress})
		}
	}
	return out
}

func inGroup(p ports.ListeningPort, name string) bool {
	for _, candidate := range []string{p.Group, p.Tag, p.RunID, p.DockerComposeProject} {
		if candidate != "" && strings.EqualFold(candidate, name) {
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
	for _, r := range plan {
		fmt.Printf("  - %s\n", describeResult(r, snapshot))
	}
	fmt.Print("\nProceed? [y/N] ")
	var answer string
	fmt.Scanln(&answer)
	return strings.EqualFold(strings.TrimSpace(answer), "y")
}

// reportKill prints the outcome and returns a non-nil error when any target
// failed, so the command exits 1 (daemon spec, "Error handling").
//
// **A target that was already gone is not a failure.** The end state the caller
// asked for — that thing is not running — already holds, so its row is reported
// as a fact and counted as stopped rather than failed. Without this, `down` on
// a project whose last process had already exited printed `2/3 stopped` and
// `1 of 3 targets failed` while everything really was stopped.
func reportKill(w io.Writer, results []killer.Result, snapshot []ports.ListeningPort, asJSON, dryRun bool) error {
	failed := 0
	for _, r := range results {
		if !r.OK && !alreadyGone(r) {
			failed++
		}
	}

	if asJSON {
		if err := writeKillJSON(w, results); err != nil {
			return err
		}
		if failed > 0 {
			return errSilent
		}
		return nil
	}

	if len(results) == 0 {
		fmt.Fprintln(w, "Nothing to stop.")
		return nil
	}
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
	name := r.Name
	if name == "" {
		name = "unknown"
	}
	var b strings.Builder
	b.WriteString(display.Bold(name))
	if r.PID > 0 {
		fmt.Fprintf(&b, " (PID %d)", r.PID)
	}
	if r.Port > 0 {
		fmt.Fprintf(&b, " on port %d", r.Port)
		if bindAmbiguous(snapshot, r.Port) && r.BindAddress != "" {
			fmt.Fprintf(&b, " [%s]", r.BindAddress)
		}
	}
	return b.String()
}

// bindAmbiguous reports whether a port number appears more than once in the
// scan, in which case the bind address is worth printing.
func bindAmbiguous(snapshot []ports.ListeningPort, port int) bool {
	seen := 0
	for _, p := range snapshot {
		if p.Port == port {
			seen++
		}
	}
	return seen > 1
}

// freedURLs lists the distinct URLs of the ports that were successfully acted
// on, in the order they appear in the results.
func freedURLs(results []killer.Result, snapshot []ports.ListeningPort) []string {
	byKey := map[string]string{}
	for i := range snapshot {
		byKey[snapshot[i].PortKey()] = snapshot[i].URL()
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range results {
		if !r.OK || r.Port == 0 {
			continue
		}
		key := r.Key()
		if seen[key] {
			continue
		}
		seen[key] = true
		if url, ok := byKey[key]; ok {
			out = append(out, url)
		}
	}
	return out
}

// writeKillJSON prints the contract §3 result list.
func writeKillJSON(w io.Writer, results []killer.Result) error {
	if results == nil {
		results = []killer.Result{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}
