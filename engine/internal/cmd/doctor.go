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

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/doctor"
	"github.com/spf13/cobra"
)

var (
	// Output and selection.
	doctorJSONFlag bool
	doctorOnlyFlag []string

	// Repair, and whether to ask first.
	doctorFixFlag bool
	doctorYesFlag bool

	// Where the project checks look.
	doctorProjectFlag string
)

var doctorCmd = &cobra.Command{
	Use:     "doctor",
	Short:   "Check that this option-berth installation is healthy",
	GroupID: commandGroupInfra,
	RunE:    doctorRun,
	Args:    cobra.NoArgs,
	// The report is the output, so a failing check must not also print a bare
	// "Error:" line beneath it; Execute still prints a real error.
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: "Run every self-check option-berth has: the binary on PATH, the config file, the\ndaemon and its socket, the database, this project's\n" +
		"oberth.yaml and docker.\n\nEach check is ok, warn, fail or skip. The exit code is 0 unless something\n" +
		"failed, so `oberth doctor` works in a script. `--fix` applies the repairs\n" +
		"that are safe to make unattended and runs the checks again.",
}

func init() {
	flags := doctorCmd.Flags()
	flags.BoolVar(&doctorJSONFlag, "json", false, "Output as JSON")
	flags.BoolVar(&doctorFixFlag, "fix", false, "Apply safe fixes to the selected project, then check again")
	flags.BoolVar(&doctorYesFlag, "yes", false, "Do not ask before applying a fix")
	flags.StringSliceVar(&doctorOnlyFlag, "only", nil, "Run only these checks (comma separated)")
	flags.StringVar(&doctorProjectFlag, "project", "", "Directory the project checks look at (default: the working directory)")
	rootCmd.AddCommand(doctorCmd)
}

func doctorRun(cmd *cobra.Command, _ []string) error {
	if unknown := doctor.UnknownSelectors(doctorOnlyFlag); len(unknown) != 0 {
		return fmt.Errorf("unknown check %s\nknown checks: %s",
			strings.Join(unknown, ", "), strings.Join(doctor.IDs(), ", "))
	}
	project, err := doctorProject()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	result := doctor.Run(ctx, doctorEnv(project), doctorOnlyFlag)

	if doctorFixFlag {
		applied, fixErr := applyDoctorFixes(ctx, cmd, result)
		if fixErr != nil {
			return fixErr
		}
		if applied > 0 {
			result = doctor.Run(ctx, doctorEnv(project), doctorOnlyFlag)
		}
	}

	switch {
	case doctorJSONFlag:
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
	default:
		renderDoctor(os.Stdout, result)
	}
	if !result.OK {
		return errSilent
	}
	return nil
}

func doctorProject() (string, error) {
	if doctorProjectFlag == "" {
		return os.Getwd()
	}
	target, err := filepath.Abs(doctorProjectFlag)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		return "", fmt.Errorf("--project %s is not a directory", doctorProjectFlag)
	}
	return target, nil
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
		Commit:  buildinfo.Commit,
		Project: project,
		Daemon:  doctorDaemonProbe,
		Docker:  doctor.DockerProbe,
	}
}

func doctorDaemonProbe(ctx context.Context) doctor.DaemonInfo {
	socket := daemon.SocketPath()
	conn, dialErr := client.Dial(ctx, client.ClientInfo{Name: "cli", Version: buildinfo.Version})
	if dialErr != nil {
		// Another protocol major is a daemon that is running, not one that is
		// absent; calling it unreachable would hide the check that explains it.
		var versionMismatch *client.ProtocolMismatchError
		if errors.As(dialErr, &versionMismatch) {
			return doctor.DaemonInfo{Reachable: true, ProtocolVersion: versionMismatch.Daemon, Socket: socket}
		}
		return doctor.DaemonInfo{Err: dialErr, Socket: socket}
	}
	defer conn.Close()

	hello := conn.Hello()
	var status rpc.DaemonStatusResult
	statusErr := conn.Call(ctx, "daemon.status", rpc.Empty{}, &status)
	probe := doctor.DaemonInfo{
		Reachable:       true,
		Socket:          hello.Socket,
		PID:             hello.PID,
		Version:         hello.DaemonVersion,
		ProtocolVersion: hello.ProtocolVersion,
		Commit:          status.Commit,
		DBPath:          status.DBPath,
		StatusErr:       statusErr,
		Observation:     greatestObservation(status.Observation, hello.Observation),
	}
	return probe
}

// greatestObservation prefers the status probe (current) over the hello cache
// (up to observationTTL old), and a reported verdict over an absent one.
func greatestObservation(current, cached *rpc.ObservationStatus) *rpc.ObservationStatus {
	if current != nil {
		return current
	}
	return cached
}

// ---------------------------------------------------------------- render ---

// doctorRow is one check as the table prints it: the glyph, id and summary
// columns, the fix hint that belongs to this row, and the evidence lines a
// failure carries. It is built before any column width is known so layout and
// content are separate decisions.
type doctorRow struct {
	check  rpc.DoctorCheck
	fix    string
	detail []string
}

func doctorRows(report rpc.DaemonDoctorResult) []doctorRow {
	rows := make([]doctorRow, 0, len(report.Checks))
	for _, check := range report.Checks {
		row := doctorRow{check: check}
		if check.Fix != "" && check.Status != doctor.StatusOK && check.Status != doctor.StatusSkip {
			row.fix = "→ " + check.Fix
		}
		// Only a failure earns its evidence in the table: it is the row a
		// reader has to act on, and config_parses' caret is useless folded
		// into one line.
		if check.Status == doctor.StatusFail && check.Detail != "" {
			row.detail = strings.Split(check.Detail, "\n")
		}
		rows = append(rows, row)
	}
	return rows
}

func renderDoctor(w io.Writer, report rpc.DaemonDoctorResult) {
	rows := doctorRows(report)
	idWidth, summaryWidth := 0, 0
	for _, row := range rows {
		if n := len(row.check.ID); n > idWidth {
			idWidth = n
		}
		if n := len(row.check.Summary); n > summaryWidth {
			summaryWidth = n
		}
	}

	counts := map[string]int{}
	for _, row := range rows {
		counts[row.check.Status]++
		line := fmt.Sprintf("%s  %s  %s", doctorGlyph(row.check.Status),
			padRight(row.check.ID, idWidth), padRight(row.check.Summary, summaryWidth))
		if row.fix != "" {
			line += "  " + display.Dim(row.fix)
		}
		fmt.Fprintln(w, strings.TrimRight(line, " "))
		for _, detail := range row.detail {
			fmt.Fprintln(w, "   "+display.Dim(detail))
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, doctorVerdict(report, counts))
}

func doctorVerdict(report rpc.DaemonDoctorResult, counts map[string]int) string {
	var summaryParts []string
	for _, status := range []string{doctor.StatusFail, doctor.StatusWarn, doctor.StatusOK, doctor.StatusSkip} {
		if n := counts[status]; n != 0 {
			summaryParts = append(summaryParts, fmt.Sprintf("%d %s", n, status))
		}
	}
	summary := strings.Join(summaryParts, ", ")

	fixable := 0
	for _, check := range report.Checks {
		if check.Fixable && (check.Status == doctor.StatusFail || check.Status == doctor.StatusWarn) {
			fixable++
		}
	}
	hint := ""
	if fixable > 0 && !doctorFixFlag {
		hint = display.Dim(fmt.Sprintf("  (`oberth doctor --fix` repairs %d of them)", fixable))
	}

	if !report.OK {
		return display.Red("something is wrong: ") + summary + hint
	}
	if counts[doctor.StatusWarn] > 0 {
		return display.Yellow("mostly healthy: ") + summary + hint
	}
	return display.Green("all good: ") + summary
}

// doctorGlyph pairs a symbol with a word: the symbol is what the eye finds in a
// colour terminal, the word is what survives NO_COLOR, a pipe and a screenshot.
func doctorGlyph(status string) string {
	glyphs := map[string]string{
		doctor.StatusOK:   display.Green("\u2714 ok  "),
		doctor.StatusWarn: display.Yellow("! warn"),
		doctor.StatusFail: display.Red("\u2716 FAIL"),
	}
	if glyph, known := glyphs[status]; known {
		return glyph
	}
	return display.Dim("\u00b7 skip")
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

// doctorFix is one repair `--fix` can apply. Each is a call the user could have
// made from the check's own hint; no repair deletes anything, and a broken
// config is moved aside rather than overwritten.
//
// Project-scoped repairs honor --project; machine configuration stays local.
type doctorFix struct {
	id  string                                                        // the check it repairs
	run func(ctx context.Context, cmd *cobra.Command) (string, error) // what it does
}

// matches reports whether this fix repairs exactly the named check. A dotted
// id names one check, not a family: a fix registered for `config_parses` must
// never be applied to some future `config_parses.<tool>` row.
func (f doctorFix) matches(id string) bool { return f.id == id }

func doctorFixes() []doctorFix {
	fixes := []doctorFix{
		{id: "config_parses", run: fixBrokenConfig},
		{id: "project_config", run: fixLegacyConfigName},
		{id: "daemon_reachable", run: restartDaemonFix},
	}
	// A fix repairs this machine's own setup only; none writes into another
	// tool's configuration.
	sort.Slice(fixes, func(i, j int) bool { return fixes[i].id < fixes[j].id })
	return fixes
}

// restartDaemonFix is the repair behind a failed daemon check.
func restartDaemonFix(_ context.Context, cmd *cobra.Command) (string, error) {
	if err := daemonRestartRun(cmd, nil); err != nil {
		return "", err
	}
	return "restarted the daemon", nil
}

// fixBrokenConfig moves an unparseable config.yaml aside and writes a fresh
// commented template in its place. The old file is the only copy of what the
// user meant to write, so it is kept.
func fixBrokenConfig(context.Context, *cobra.Command) (string, error) {
	configPath := config.Path()
	aside := configPath + doctor.BrokenSuffix + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(configPath, aside); err != nil {
		return "", fmt.Errorf("could not move %s aside: %w", configPath, err)
	}
	if err := config.WriteTemplate(true); err != nil {
		return "", err
	}
	return fmt.Sprintf("moved the broken config to %s and wrote a fresh one", aside), nil
}

// selectDoctorFixes is the repair list for the checks that failed or warned
// and advertised themselves as fixable, in report order. A fixable check with
// no registered repair contributes nothing here; --fix cannot invent one.
func selectDoctorFixes(report rpc.DaemonDoctorResult) []doctorFix {
	fixes := doctorFixes()
	var todo []doctorFix
	for _, check := range report.Checks {
		if !check.Fixable || check.Status == doctor.StatusOK || check.Status == doctor.StatusSkip {
			continue
		}
		for _, fix := range fixes {
			if fix.matches(check.ID) {
				todo = append(todo, fix)
			}
		}
	}
	return todo
}

// applyDoctorFixes runs every safe fix for a check that is not ok, after
// confirmation. It returns how many it applied.
func applyDoctorFixes(ctx context.Context, cmd *cobra.Command, report rpc.DaemonDoctorResult) (int, error) {
	planned := selectDoctorFixes(report)
	if len(planned) == 0 {
		fmt.Fprintln(os.Stderr, "nothing to fix automatically")
		return 0, nil
	}

	fmt.Fprintf(os.Stderr, "oberth doctor --fix will:\n")
	for _, fix := range planned {
		fmt.Fprintf(os.Stderr, "  repair %s\n", fix.id)
	}
	if !doctorYesFlag && !confirmDoctorFix() {
		fmt.Fprintln(os.Stderr, "nothing was changed")
		return 0, nil
	}

	applied := 0
	for _, fix := range planned {
		what, runErr := fix.run(ctx, cmd)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "could not repair %s: %v\n", fix.id, runErr)
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", fix.id, what)
		applied++
	}
	fmt.Fprintln(os.Stderr)
	return applied, nil
}

// confirmDoctorFix asks once on the terminal. With a pipe or a CI job there is
// nobody to ask, so the answer is no; `--yes` is the way past it.
func confirmDoctorFix() bool {
	stat, statErr := os.Stdin.Stat()
	if statErr != nil || stat.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(os.Stderr, "stdin is not a terminal; pass --yes to apply these")
		return false
	}
	fmt.Fprint(os.Stderr, "apply? [y/N] ")
	typed, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
	if readErr != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(typed))
	return answer == "y" || answer == "yes"
}
