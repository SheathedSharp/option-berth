package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/feedback"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/terminal"
	"github.com/sheathedsharp/option-berth/engine/internal/versioncheck"
	"github.com/spf13/cobra"
)

// feedbackOffEnv turns the first-screen question and sending off for one
// process or for good from a shell profile. Presence disables, the way
// NO_COLOR and BERTH_NO_UPDATE_CHECK work; the explicit false spellings are
// there so a profile can also set it to 0.
const feedbackOffEnv = "BERTH_NO_FEEDBACK"

var (
	feedbackStatusJSON bool
	feedbackOnJSON     bool
	feedbackOffJSON    bool
	feedbackReportJSON bool
	feedbackLogJSON    bool

	feedbackKindFlag        string
	feedbackCommandFlag     string
	feedbackErrorCodeFlag   string
	feedbackObservationFlag string
	feedbackElapsedFlag     string
	feedbackServicesFlag    int
	feedbackNoteFlag        string
	feedbackYesFlag         bool
	feedbackDryRunFlag      bool
	feedbackOpenFlag        bool
)

// newFeedbackCommand builds the `oberth feedback` group. The commands are
// created here rather than as package-level vars so the flag wiring stays in
// one place.
func newFeedbackCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "feedback",
		Short: "Send an optional report about using oberth",
		Long: "Report a problem with oberth itself, with the facts the command already has.\n" +
			"Off until `oberth feedback on` (or the first screen's answer) says otherwise; a\n" +
			"free-text note is shown and confirmed before it is sent. Reports carry the\n" +
			"version, platform, command name, error code and timing bucket -- never paths,\n" +
			"logs, commands, manifest content or environment variables.",
		GroupID: commandGroupInfra,
	}
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether reports are on, who files them, and how many there are",
		Long: "Report the recorded decision, the GitHub account `gh` is signed in as, the\n" +
			"local count of reports this installation produced, and the daily limit. It\n" +
			"reads the local gh credential only; `oberth feedback log` reads each filed\n" +
			"issue's state through the same login.",
		Args: cobra.NoArgs,
		RunE: runFeedbackStatus,
	}
	onCmd := &cobra.Command{
		Use:   "on",
		Short: "Allow improvement reports",
		Args:  cobra.NoArgs,
		RunE:  runFeedbackOn,
	}
	offCmd := &cobra.Command{
		Use:   "off",
		Short: "Stop improvement reports and stop asking",
		Args:  cobra.NoArgs,
		RunE:  runFeedbackOff,
	}
	reportCmd := &cobra.Command{
		Use:   "report",
		Short: "Compose one report and file it",
		Long: "Compose a report from facts the caller states and file it: through `gh` with\n" +
			"the user's own account when it is signed in, otherwise as a prefilled issue URL\n" +
			"the person submits. On a terminal without --kind/--command it asks for them\n" +
			"from the same fixed vocabulary with the arrow keys, then shows the exact\n" +
			"report before sending. The same complaint from the same build is filed at\n" +
			"most once a day, and one installation files at most five reports a day.",
		Args: cobra.NoArgs,
		RunE: runFeedbackReport,
	}
	logCmd := &cobra.Command{
		Use:   "log",
		Short: "Show what this installation reported and what happened to each issue",
		Long: "List the reports this installation produced with the issue's state, close date\n" +
			"and labels, read through the person's own gh login. Without a signed-in gh the\n" +
			"local record still prints and each filed report says why its state is unknown.\n" +
			"--json carries the same data plus a summary, for an agent reading the loop.",
		Args: cobra.NoArgs,
		RunE: runFeedbackLog,
	}

	reportCmd.Flags().StringVar(&feedbackKindFlag, "kind", "",
		"what went wrong: "+strings.Join(feedback.KindNames(), ", "))
	reportCmd.Flags().StringVar(&feedbackCommandFlag, "command", "",
		"the oberth command it happened in: "+strings.Join(feedback.CommandNames(), ", "))
	reportCmd.Flags().StringVar(&feedbackErrorCodeFlag, "error-code", "",
		"the stable error code the command returned, when it returned one")
	reportCmd.Flags().StringVar(&feedbackObservationFlag, "observation", "",
		"the observation.state status published: observable, partial, blind, bind_denied")
	reportCmd.Flags().StringVar(&feedbackElapsedFlag, "elapsed", "",
		"how long it took, such as 45s or 3m; it is filed as a bucket, not a number")
	reportCmd.Flags().IntVar(&feedbackServicesFlag, "services", -1,
		"the declared service count, filed as a bucket")
	reportCmd.Flags().StringVar(&feedbackNoteFlag, "note", "",
		"what you saw, in your own words; redacted, shown and confirmed before sending")
	reportCmd.Flags().BoolVar(&feedbackYesFlag, "yes", false,
		"send the note without asking again (it is still redacted)")
	reportCmd.Flags().BoolVar(&feedbackDryRunFlag, "dry-run", false,
		"print the exact report and send nothing")
	reportCmd.Flags().BoolVar(&feedbackOpenFlag, "open", false,
		"open the prefilled issue page when gh is not used")
	reportCmd.Flags().BoolVar(&feedbackReportJSON, "json", false, "Output as JSON")

	statusCmd.Flags().BoolVar(&feedbackStatusJSON, "json", false, "Output as JSON")
	onCmd.Flags().BoolVar(&feedbackOnJSON, "json", false, "Output as JSON")
	offCmd.Flags().BoolVar(&feedbackOffJSON, "json", false, "Output as JSON")
	logCmd.Flags().BoolVar(&feedbackLogJSON, "json", false, "Output as JSON")

	group.AddCommand(statusCmd, onCmd, offCmd, reportCmd, logCmd)
	return group
}

var feedbackCmd = newFeedbackCommand()

func init() {
	rootCmd.AddCommand(feedbackCmd)
}

// feedbackDecision names the answer `status` reports: the three recorded
// decisions plus "unasked", which is the absence of a record.
func feedbackDecision(consent feedback.Consent, present bool, loadErr error) string {
	if !present || loadErr != nil {
		return "unasked"
	}
	return string(consent.Decision)
}

// feedbackDisabledByEnv is the one place the environment switch is read.
func feedbackDisabledByEnv() bool {
	switch strings.TrimSpace(strings.ToLower(os.Getenv(feedbackOffEnv))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func runFeedbackStatus(cmd *cobra.Command, _ []string) error {
	consent, present, loadErr := feedback.LoadConsent(paths.FeedbackPath())
	entries, _ := feedback.ReadLedger(paths.FeedbackLedgerPath())
	account := feedback.GHLogin(cmd.Context())
	totals := feedback.Totals(entries)
	now := time.Now()

	channel := "url"
	if account.Available {
		channel = "gh"
	}
	out := struct {
		Schema        string                `json:"schema"`
		Decision      string                `json:"decision"`
		Enabled       bool                  `json:"enabled"`
		DisabledByEnv bool                  `json:"disabled_by_env"`
		Channel       string                `json:"channel"`
		GitHub        feedback.GHAccount    `json:"github"`
		Reports       feedback.LedgerTotals `json:"reports"`
		Path          string                `json:"path"`
		LedgerPath    string                `json:"ledger_path"`
		Reports24h    int                   `json:"reports_24h"`
		DailyLimit    int                   `json:"daily_limit"`
	}{
		Schema:        feedback.ConsentSchema,
		Decision:      feedbackDecision(consent, present, loadErr),
		Enabled:       loadErr == nil && present && consent.Enabled() && !feedbackDisabledByEnv(),
		DisabledByEnv: feedbackDisabledByEnv(),
		Channel:       channel,
		GitHub:        account,
		Reports:       totals,
		Path:          paths.FeedbackPath(),
		LedgerPath:    paths.FeedbackLedgerPath(),
		Reports24h:    feedback.CountSince(entries, now, 24*time.Hour),
		DailyLimit:    feedback.DailyLimit,
	}

	if feedbackStatusJSON {
		return printJSON(out)
	}
	fmt.Printf("decision   %s\n", out.Decision)
	if loadErr != nil {
		fmt.Printf("record     %s\n", display.Yellow("unreadable: "+loadErr.Error()))
	}
	fmt.Printf("sending    %s\n", yesNo(out.Enabled))
	// Who files a report is part of the decision, so it is on the status
	// screen rather than a line the person only meets after composing one.
	fmt.Printf("github     %s\n", githubLine(account))
	fmt.Printf("recorded   %s\n", out.Path)
	fmt.Printf("reports    %s\n", feedbackTotalsLine(totals))
	if totals.Filed > 0 {
		fmt.Printf("           `oberth feedback log` shows each issue's state\n")
	}
	fmt.Printf("last 24h   %d of %d reports\n", out.Reports24h, out.DailyLimit)
	if feedbackDisabledByEnv() {
		fmt.Printf("environment %s is set, so nothing is sent or asked\n", feedbackOffEnv)
	}
	return nil
}

// githubLine is the one sentence a person reads about who files the report.
func githubLine(account feedback.GHAccount) string {
	switch {
	case account.Available && account.Account != "":
		return "signed in as " + account.Account
	case account.Available:
		return "signed in"
	default:
		return "not available (" + account.Reason + ")"
	}
}

// feedbackTotalsLine is the local half: how many reports this installation
// produced and how many of them reached GitHub. Issue state is `feedback log`,
// because reading it needs a live gh.
func feedbackTotalsLine(totals feedback.LedgerTotals) string {
	if totals.Total == 0 {
		return "none recorded"
	}
	return fmt.Sprintf("%d recorded · %d filed · %d prefilled · last %s",
		totals.Total, totals.Filed, totals.Prefilled, shortDate(totals.LastAt))
}

// shortDate is an RFC3339 timestamp as a calendar day. An unparsable value is
// left as written rather than replaced with a guess.
func shortDate(at string) string {
	if parsed, err := time.Parse(time.RFC3339, at); err == nil {
		return parsed.Format("2006-01-02")
	}
	return at
}

func yesNo(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func runFeedbackOn(cmd *cobra.Command, _ []string) error {
	consent := feedback.Decide(feedback.DecisionEnabled, buildinfo.VersionValue(), time.Now())
	if err := feedback.WriteConsent(paths.FeedbackPath(), consent); err != nil {
		return err
	}
	account := feedback.GHLogin(cmd.Context())
	if feedbackOnJSON {
		return printJSON(map[string]any{
			"schema":   feedback.ConsentSchema,
			"decision": string(feedback.DecisionEnabled),
			"github":   account,
			"path":     paths.FeedbackPath(),
		})
	}
	fmt.Printf("improvement reports are on; the decision is recorded in %s\n", paths.FeedbackPath())
	// Turning it on is the moment the person can still change who files it, so
	// the account is named here rather than only when a report is composed.
	if account.Available {
		fmt.Printf("github     %s; a filed report uses this account\n", githubLine(account))
	} else {
		fmt.Printf("github     %s; a report opens a prefilled issue page you submit\n", githubLine(account))
	}
	fmt.Println(display.Dim("nothing is sent until a report is filed, and a free-text note is always shown first"))
	return nil
}

func runFeedbackOff(*cobra.Command, []string) error {
	consent := feedback.Decide(feedback.DecisionDeclined, buildinfo.VersionValue(), time.Now())
	if err := feedback.WriteConsent(paths.FeedbackPath(), consent); err != nil {
		return err
	}
	if feedbackOffJSON {
		return printJSON(map[string]any{
			"schema":   feedback.ConsentSchema,
			"decision": string(feedback.DecisionDeclined),
			"path":     paths.FeedbackPath(),
		})
	}
	fmt.Println("improvement reports are off; the first screen will not ask again")
	return nil
}

func runFeedbackReport(cmd *cobra.Command, _ []string) error {
	if feedbackDisabledByEnv() {
		return failHint("invalid_config",
			"improvement reports are disabled by "+feedbackOffEnv,
			"unset "+feedbackOffEnv+" to file one")
	}
	consent, present, err := feedback.LoadConsent(paths.FeedbackPath())
	if err != nil {
		return failHint("invalid_config", err.Error(),
			"run `oberth feedback off` and then `oberth feedback on` to rewrite the record")
	}
	if !present || !consent.Enabled() {
		return failHint("invalid_config",
			"improvement reports are off for this installation",
			"run `oberth feedback on` if you want to send this one")
	}

	// With no kind or command and a terminal to draw on, ask instead of
	// refusing: the fixed vocabulary is easier to pick than to memorise.
	composed := false
	if feedbackComposeApplies() {
		if err := composeFeedback(); err != nil {
			if errors.Is(err, terminal.ErrCancelled) {
				fmt.Fprintln(cmd.OutOrStdout(), "cancelled; nothing was sent")
				return nil
			}
			return err
		}
		composed = true
	}

	report, err := feedback.Build(feedback.Input{
		Kind:        feedbackKindFlag,
		Command:     feedbackCommandFlag,
		ErrorCode:   feedbackErrorCodeFlag,
		Observation: feedbackObservationFlag,
		Elapsed:     feedbackElapsedFlag,
		Services:    feedbackServicesFlag,
		Note:        feedbackNoteFlag,
		Version:     buildinfo.VersionValue(),
		Commit:      buildinfo.Commit,
		Platform:    feedback.Platform(),
		Now:         time.Now(),
		Redactor:    feedback.NewRedactor(),
	})
	if err != nil {
		return usageError{err}
	}

	now := time.Now()
	entries, _ := feedback.ReadLedger(paths.FeedbackLedgerPath())
	if !feedbackDryRunFlag {
		// The limits are checked before the note is shown: asking someone to
		// confirm a report that will be refused wastes the very round trip this
		// feature exists to remove.
		if duplicate, ok := feedback.DuplicateSince(entries, report.Fingerprint(), now, feedback.DuplicateWindow); ok {
			message := fmt.Sprintf("the same report was filed at %s; nothing was sent", duplicate.At)
			if feedbackReportJSON {
				return printJSON(map[string]any{
					"schema": feedback.ReportSchema, "sent": false,
					"reason": "duplicate", "url": duplicate.URL,
				})
			}
			fmt.Println(message)
			if duplicate.URL != "" {
				fmt.Println(duplicate.URL)
			}
			return nil
		}
		if count := feedback.CountSince(entries, now, 24*time.Hour); count >= feedback.DailyLimit {
			return failHint("permission_denied",
				fmt.Sprintf("%d reports were filed in the last day, which is the limit", count),
				"nothing was sent and nothing was recorded; existing records are at "+paths.FeedbackLedgerPath())
		}
		// A note is the reporter's own words, so it never leaves the machine
		// without the person seeing it. A report composed interactively gets
		// the same look even without a note: the person just built it, so the
		// last step should show the exact thing that will be filed.
		if (report.Note != "" || composed) && !feedbackYesFlag {
			if !confirmFeedbackReport(report.Preview()) {
				return usageError{errors.New("nothing was sent: the report needs confirmation, or pass --yes after reading it with --dry-run")}
			}
		}
	}

	if feedbackDryRunFlag {
		if feedbackReportJSON {
			return printJSON(map[string]any{
				"schema":      feedback.ReportSchema,
				"sent":        false,
				"reason":      "dry-run",
				"title":       report.Title(),
				"body":        report.Body(),
				"fingerprint": report.Fingerprint(),
			})
		}
		fmt.Println(report.Preview())
		return nil
	}

	delivery := feedback.Deliver(cmd.Context(), feedback.DefaultRepo, report)
	if delivery.Channel == "url" && delivery.Reason != "" {
		fmt.Fprintln(os.Stderr, display.Dim("gh was not used: "+delivery.Reason))
	}
	if err := feedback.AppendLedger(paths.FeedbackLedgerPath(), feedback.Entry{
		At:          now.UTC().Format(time.RFC3339),
		Fingerprint: report.Fingerprint(),
		Kind:        string(report.Kind),
		Command:     string(report.Command),
		Version:     report.Version,
		Channel:     delivery.Channel,
		URL:         delivery.URL,
		Note:        report.Note != "",
	}); err != nil {
		// The report is already out; the ledger is advisory and says so.
		fmt.Fprintln(os.Stderr, display.Dim("the report went out but the local record could not be written: "+err.Error()))
	}

	if delivery.Channel == "url" && feedbackOpenFlag {
		if err := feedback.OpenBrowser(cmd.Context(), delivery.URL); err != nil {
			fmt.Fprintln(os.Stderr, display.Dim(err.Error()))
		}
	}

	if feedbackReportJSON {
		body := map[string]any{
			"schema":      feedback.ReportSchema,
			"sent":        delivery.Channel == "gh",
			"channel":     delivery.Channel,
			"url":         delivery.URL,
			"fingerprint": report.Fingerprint(),
		}
		if delivery.Channel != "gh" {
			// A prefilled page is not a filed report: the person still has to
			// press submit, so the answer says so instead of reporting success.
			body["reason"] = delivery.Reason
		}
		return printJSON(body)
	}
	if delivery.Channel == "gh" {
		fmt.Printf("filed %s\n", delivery.URL)
		return nil
	}
	fmt.Println("open this page and press submit to file the report:")
	fmt.Println(delivery.URL)
	return nil
}

// confirmFeedbackReport shows the exact report on stderr — --json stdout must
// stay one value — and asks once. A non-terminal answers no.
func confirmFeedbackReport(preview string) bool {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(os.Stderr, "stdin is not a terminal; pass --yes to send the report after reading it with --dry-run")
		return false
	}
	fmt.Fprintln(os.Stderr, preview)
	fmt.Fprint(os.Stderr, "send this report? [y/N] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// runFeedbackLog is the response half of the feedback loop: what this
// installation reported, and what happened to each issue. It reads GitHub
// through the person's own gh login; without gh the local record still prints
// and every filed report says why its state is unknown.
func runFeedbackLog(cmd *cobra.Command, _ []string) error {
	entries, err := feedback.ReadLedger(paths.FeedbackLedgerPath())
	if err != nil {
		return err
	}
	account := feedback.GHLogin(cmd.Context())
	lookup := feedback.LookupIssues(cmd.Context(), account, feedback.DefaultRepo, feedback.FiledIssueNumbers(entries))
	logged := feedback.LogEntries(entries, lookup)
	outcomes := make([]feedback.Outcome, len(logged))
	for i := range logged {
		outcomes[i] = logged[i].Outcome
	}
	summary := feedback.Summarize(entries, outcomes)

	shown := logged
	if len(shown) > feedback.LedgerShow {
		shown = shown[len(shown)-feedback.LedgerShow:]
	}
	if feedbackLogJSON {
		return printJSON(struct {
			Schema      string                 `json:"schema"`
			GitHub      feedback.GHAccount     `json:"github"`
			Summary     feedback.ReportSummary `json:"summary"`
			Entries     []feedback.LogEntry    `json:"entries"`
			Path        string                 `json:"path"`
			IssueReason string                 `json:"issue_reason,omitempty"`
		}{
			Schema:      feedback.ReportSchema,
			GitHub:      account,
			Summary:     summary,
			Entries:     shown,
			Path:        paths.FeedbackLedgerPath(),
			IssueReason: lookup.Reason,
		})
	}

	fmt.Printf("github     %s\n", githubLine(account))
	fmt.Printf("reports    %s\n", feedbackTotalsLine(summary.LedgerTotals))
	if summary.Filed > 0 {
		fmt.Printf("issues     %s\n", feedbackOutcomeLine(summary))
		if !account.Available {
			fmt.Printf("           `gh auth login` is needed to read issue state\n")
		}
	}
	if len(shown) == 0 {
		fmt.Println()
		fmt.Println("no reports have been filed from this installation")
		return nil
	}
	if len(shown) < len(logged) {
		fmt.Printf("           showing the last %d of %d\n", len(shown), len(logged))
	}
	for _, item := range shown {
		fmt.Println()
		printFeedbackEntry(item)
	}
	return nil
}

// feedbackOutcomeLine is the closed set of issue outcomes, in the order a
// person reads them: still open, then the two ways it can be closed, then the
// reports whose state could not be read.
func feedbackOutcomeLine(summary feedback.ReportSummary) string {
	return fmt.Sprintf("%d open · %d resolved · %d closed as not planned · %d unknown",
		summary.Open, summary.Resolved, summary.NotPlanned, summary.Unknown)
}

func printFeedbackEntry(item feedback.LogEntry) {
	number := "page"
	if item.Issue != nil {
		number = fmt.Sprintf("#%d", item.Issue.Number)
	} else if parsed, ok := feedback.IssueNumber(item.URL); ok {
		number = fmt.Sprintf("#%d", parsed)
	}
	fmt.Printf("%-6s %s · %s · %s · %s\n", number, item.Kind, item.Command, item.Version, shortDate(item.At))
	fmt.Printf("  %s\n", feedbackOutcomeText(item.Outcome))
	if item.URL != "" {
		fmt.Printf("  %s\n", item.URL)
	}
}

// feedbackOutcomeText is one line per entry: the state word, then the GitHub
// context that makes it checkable (close date, last update, labels).
func feedbackOutcomeText(outcome feedback.Outcome) string {
	switch outcome.Status {
	case feedback.OutcomeOpen:
		if outcome.Issue == nil {
			return "open"
		}
		return "open" + issueContext(outcome.Issue)
	case feedback.OutcomeResolved:
		if outcome.Issue == nil {
			return "resolved"
		}
		return "resolved" + issueContext(outcome.Issue)
	case feedback.OutcomeNotPlanned:
		if outcome.Issue == nil {
			return "closed as not planned"
		}
		return "closed as not planned" + issueContext(outcome.Issue)
	case feedback.OutcomeNotFiled:
		return "prefilled page only; not confirmed filed"
	default:
		return outcome.ReasonText()
	}
}

func issueContext(issue *feedback.Issue) string {
	var parts []string
	if issue.ClosedAt != "" {
		parts = append(parts, "closed "+shortDate(issue.ClosedAt))
	} else if issue.UpdatedAt != "" {
		parts = append(parts, "updated "+shortDate(issue.UpdatedAt))
	}
	if len(issue.Labels) > 0 {
		parts = append(parts, "labels: "+strings.Join(issue.Labels, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

// askFeedbackOnce is the first screen's question. It asks at most once per
// version, never in a pipe, and never in a build that is not a release: a
// report from a development build has no version to improve.
func askFeedbackOnce() {
	if !feedbackPromptApplies(os.Stdin, os.Stdout) {
		return
	}
	consent, present, err := feedback.LoadConsent(paths.FeedbackPath())
	if err != nil {
		present = false
	}
	if !feedback.ShouldAsk(consent, present, buildinfo.VersionValue()) {
		return
	}
	decision, answered := promptFeedbackDecision()
	if !answered {
		return
	}
	record := feedback.Decide(decision, buildinfo.VersionValue(), time.Now())
	if err := feedback.WriteConsent(paths.FeedbackPath(), record); err != nil {
		fmt.Fprintln(os.Stderr, display.Dim("could not record the answer: "+err.Error()))
		return
	}
	switch decision {
	case feedback.DecisionEnabled:
		fmt.Println(display.Dim("  improvement reports are on; change this with `oberth feedback off`"))
	case feedback.DecisionDeclined:
		fmt.Println(display.Dim("  improvement reports are off; change this with `oberth feedback on`"))
	default:
		fmt.Println(display.Dim("  skipped; the next version will ask once more"))
	}
}

// feedbackPromptApplies is the whole gate for the question: both ends of the
// conversation are this terminal, the environment has not switched it off, and
// this build is a release.
func feedbackPromptApplies(stdin, stdout *os.File) bool {
	if feedbackDisabledByEnv() {
		return false
	}
	if !versioncheck.IsRelease(buildinfo.VersionValue()) {
		return false
	}
	for _, file := range []*os.File{stdin, stdout} {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

// promptFeedbackDecision asks the one question and reads the answer. An empty
// line, end of input or anything unrecognized means "skip": no record is
// written, so the question returns on the next version rather than inventing
// consent.
func promptFeedbackDecision() (feedback.Decision, bool) {
	fmt.Println()
	fmt.Println("  " + display.Bold("Help improve option-berth?"))
	fmt.Println("    With your permission, oberth files a redacted report when a fixed problem")
	fmt.Println("    pattern shows up: version, platform, command, error code and a timing")
	fmt.Println("    bucket — never paths, logs, commands, manifest content or environment.")
	fmt.Println("    It goes through your own GitHub account, so your username is visible;")
	fmt.Println("    without gh you get a prefilled issue page instead.")
	fmt.Println("    Change this any time with `oberth feedback on|off`.")
	fmt.Println()
	fmt.Print("    Send improvement reports? [y]es / [n]o / [s]kip (default): ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Println()
		return feedback.DecisionSkipped, false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return feedback.DecisionEnabled, true
	case "n", "no":
		return feedback.DecisionDeclined, true
	default:
		return feedback.DecisionSkipped, true
	}
}
