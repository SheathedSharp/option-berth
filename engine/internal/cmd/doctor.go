package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/internal/config"
	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/doctor"
	"github.com/spf13/cobra"
)

var (
	doctorJSONFlag    bool
	doctorFixFlag     bool
	doctorYesFlag     bool
	doctorOnlyFlag    []string
	doctorProjectFlag string
)

var doctorCmd = &cobra.Command{
	Use:     "doctor",
	Short:   "Check that this option-berth installation is healthy",
	GroupID: commandGroupSupport,
	Long: "Run every self-check option-berth has: the binary on PATH, the config file, the\n" +
		"daemon and its socket, the database, this project's\n" +
		"oberth.yaml and docker.\n\n" +
		"Each check is ok, warn, fail or skip. The exit code is 0 unless something\n" +
		"failed, so `oberth doctor` works in a script. `--fix` applies the repairs\n" +
		"that are safe to make unattended and runs the checks again.",
	Args: cobra.NoArgs,
	// The report is the output; a failing check must not also print a bare
	// "Error:" line under the table. Execute() still prints a real error.
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          doctorRun,
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSONFlag, "json", false, "Output as JSON")
	doctorCmd.Flags().BoolVar(&doctorFixFlag, "fix", false,
		"Apply the safe fixes from the working directory, then check again")
	doctorCmd.Flags().BoolVar(&doctorYesFlag, "yes", false, "Do not ask before applying a fix")
	doctorCmd.Flags().StringSliceVar(&doctorOnlyFlag, "only", nil,
		"Run only these checks (comma separated)")
	doctorCmd.Flags().StringVar(&doctorProjectFlag, "project", "",
		"Directory the project checks look at (default: the working directory)")
	rootCmd.AddCommand(doctorCmd)
}

func doctorRun(cmd *cobra.Command, _ []string) error {
	if bad := doctor.UnknownSelectors(doctorOnlyFlag); len(bad) > 0 {
		return fmt.Errorf("unknown check %s\nknown checks: %s",
			strings.Join(bad, ", "), strings.Join(doctor.IDs(), ", "))
	}
	project, err := doctorProject()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	env := doctorEnv(project)
	report := doctor.Run(ctx, env, doctorOnlyFlag)

	if doctorFixFlag {
		applied, err := applyDoctorFixes(ctx, cmd, report)
		if err != nil {
			return err
		}
		if applied > 0 {
			report = doctor.Run(ctx, doctorEnv(project), doctorOnlyFlag)
		}
	}

	if doctorJSONFlag {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
	} else {
		renderDoctor(os.Stdout, report)
	}
	if !report.OK {
		return errSilent
	}
	return nil
}

func doctorProject() (string, error) {
	if doctorProjectFlag == "" {
		return os.Getwd()
	}
	abs, err := filepath.Abs(doctorProjectFlag)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("--project %s is not a directory", doctorProjectFlag)
	}
	return abs, nil
}

// doctorEnv is the CLI's view of the world: the running binary, docker, and a
// daemon dialled once without ever autostarting one — a diagnosis must report
// the machine as it found it, not as it left it.
//
// Nothing here reaches the network. This fork is built from source, so there is
// no release feed for a check to consult.
func doctorEnv(project string) doctor.Env {
	return doctor.Env{
		Mode:    doctor.ModeCLI,
		Version: buildinfo.Version,
		Project: project,
		Daemon:  doctorDaemonProbe,
		Docker:  doctor.DockerProbe,
	}
}

func doctorDaemonProbe(ctx context.Context) doctor.DaemonInfo {
	socket := daemon.SocketPath()
	c, err := client.Dial(ctx, client.ClientInfo{Name: "cli", Version: buildinfo.Version})
	if err != nil {
		// A daemon speaking another protocol major is running, not absent:
		// reporting it as unreachable would hide the one check that explains
		// what is actually wrong.
		var mismatch *client.ProtocolMismatchError
		if errors.As(err, &mismatch) {
			return doctor.DaemonInfo{Reachable: true, ProtocolVersion: mismatch.Daemon, Socket: socket}
		}
		return doctor.DaemonInfo{Err: err, Socket: socket}
	}
	defer c.Close()

	hello := c.Hello()
	var status rpc.DaemonStatusResult
	_ = c.Call(ctx, "daemon.status", rpc.Empty{}, &status)
	info := doctor.DaemonInfo{
		Reachable:       true,
		Version:         hello.DaemonVersion,
		ProtocolVersion: hello.ProtocolVersion,
		Socket:          hello.Socket,
		PID:             hello.PID,
		DBPath:          status.DBPath,
	}
	return info
}

// ---------------------------------------------------------------- render ---

func renderDoctor(w io.Writer, report rpc.DaemonDoctorResult) {
	idWidth, summaryWidth := 0, 0
	for _, c := range report.Checks {
		if len(c.ID) > idWidth {
			idWidth = len(c.ID)
		}
		if len(c.Summary) > summaryWidth {
			summaryWidth = len(c.Summary)
		}
	}

	counts := map[string]int{}
	for _, c := range report.Checks {
		counts[c.Status]++
		row := fmt.Sprintf("%s  %s  %s", doctorGlyph(c.Status),
			padRight(c.ID, idWidth), padRight(c.Summary, summaryWidth))
		if c.Fix != "" && c.Status != doctor.StatusOK && c.Status != doctor.StatusSkip {
			row += "  " + display.Dim("→ "+c.Fix)
		}
		fmt.Fprintln(w, strings.TrimRight(row, " "))
		// Only a failure earns its evidence in the table: it is the row a
		// reader has to act on, and config_parses' caret is useless folded
		// into one line.
		if c.Status == doctor.StatusFail && c.Detail != "" {
			for _, line := range strings.Split(c.Detail, "\n") {
				fmt.Fprintln(w, "   "+display.Dim(line))
			}
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, doctorVerdict(report, counts))
}

func doctorVerdict(report rpc.DaemonDoctorResult, counts map[string]int) string {
	var parts []string
	for _, status := range []string{doctor.StatusFail, doctor.StatusWarn, doctor.StatusOK, doctor.StatusSkip} {
		if n := counts[status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, status))
		}
	}
	summary := strings.Join(parts, ", ")

	fixable := 0
	for _, c := range report.Checks {
		if c.Fixable && (c.Status == doctor.StatusFail || c.Status == doctor.StatusWarn) {
			fixable++
		}
	}
	hint := ""
	if fixable > 0 && !doctorFixFlag {
		hint = display.Dim(fmt.Sprintf("  (`oberth doctor --fix` repairs %d of them)", fixable))
	}

	switch {
	case !report.OK:
		return display.Red("something is wrong: ") + summary + hint
	case counts[doctor.StatusWarn] > 0:
		return display.Yellow("mostly healthy: ") + summary + hint
	default:
		return display.Green("all good: ") + summary
	}
}

// doctorGlyph is a symbol and a word, not one or the other: the symbol is what
// the eye finds when the terminal has colour, and the word is what survives
// NO_COLOR, a pipe and a screenshot.
func doctorGlyph(status string) string {
	switch status {
	case doctor.StatusOK:
		return display.Green("\u2714 ok  ")
	case doctor.StatusWarn:
		return display.Yellow("! warn")
	case doctor.StatusFail:
		return display.Red("\u2716 FAIL")
	default:
		return display.Dim("\u00b7 skip")
	}
}

// padRight pads to a width counted in runes, not bytes: paths and labels carry
// non-ASCII, and a column that measures bytes drifts as soon as one does.
func padRight(s string, width int) string {
	n := len([]rune(s))
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

// ------------------------------------------------------------------- fix ---

// doctorFix is one repair `--fix` may apply. Every one of them is a call the
// user could have made themselves from the check's own fix hint; nothing here
// deletes anything, and the broken config is moved aside rather than replaced.
//
// The install commands resolve project scope from the working directory, the
// way they do when a user types them, so `--fix` repairs the project you run it
// in — not the one `--project` pointed the checks at.
type doctorFix struct {
	// id is the check it repairs; a dotted id matches by prefix too.
	id  string
	run func(ctx context.Context, cmd *cobra.Command) (string, error)
}

func doctorFixes() []doctorFix {
	fixes := []doctorFix{
		{id: "config_parses", run: fixBrokenConfig},
		{id: "project_config", run: fixLegacyConfigName},
		{id: "daemon_reachable", run: func(ctx context.Context, cmd *cobra.Command) (string, error) {
			if err := daemonRestartRun(cmd, nil); err != nil {
				return "", err
			}
			return "restarted the daemon", nil
		}},
	}
	// Fixes repair this machine's own setup only; nothing here writes into
	// another tool's configuration.
	sort.Slice(fixes, func(i, j int) bool { return fixes[i].id < fixes[j].id })
	return fixes
}

// fixBrokenConfig moves an unparseable config.yaml aside and writes a fresh
// commented template in its place. The old file is kept: it is the only copy of
// whatever the user meant to write.
func fixBrokenConfig(context.Context, *cobra.Command) (string, error) {
	path := config.Path()
	stamp := time.Now().UTC().Format("20060102T150405Z")
	moved := path + doctor.BrokenSuffix + stamp
	if err := os.Rename(path, moved); err != nil {
		return "", fmt.Errorf("could not move %s aside: %w", path, err)
	}
	if err := config.WriteTemplate(true); err != nil {
		return "", err
	}
	return fmt.Sprintf("moved the broken config to %s and wrote a fresh one", moved), nil
}

// applyDoctorFixes runs every safe fix for a check that is not ok, after
// confirmation. It returns how many it applied.
func applyDoctorFixes(ctx context.Context, cmd *cobra.Command, report rpc.DaemonDoctorResult) (int, error) {
	var todo []doctorFix
	for _, c := range report.Checks {
		if !c.Fixable || c.Status == doctor.StatusOK || c.Status == doctor.StatusSkip {
			continue
		}
		for _, f := range doctorFixes() {
			if f.id == c.ID {
				todo = append(todo, f)
			}
		}
	}
	if len(todo) == 0 {
		fmt.Fprintln(os.Stderr, "nothing to fix automatically")
		return 0, nil
	}

	fmt.Fprintf(os.Stderr, "oberth doctor --fix will:\n")
	for _, f := range todo {
		fmt.Fprintf(os.Stderr, "  repair %s\n", f.id)
	}
	if !doctorYesFlag && !confirmDoctorFix() {
		fmt.Fprintln(os.Stderr, "nothing was changed")
		return 0, nil
	}

	applied := 0
	for _, f := range todo {
		what, err := f.run(ctx, cmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not repair %s: %v\n", f.id, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", f.id, what)
		applied++
	}
	fmt.Fprintln(os.Stderr)
	return applied, nil
}

// confirmDoctorFix asks once on the terminal. A non-interactive run (a pipe, a
// CI job) answers no rather than blocking, which is why `--yes` exists.
func confirmDoctorFix() bool {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(os.Stderr, "stdin is not a terminal; pass --yes to apply these")
		return false
	}
	fmt.Fprint(os.Stderr, "apply? [y/N] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
