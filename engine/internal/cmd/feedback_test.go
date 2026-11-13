package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/feedback"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
)

// feedbackTestEnv isolates the two files a feedback command touches and clears
// the environment switch. Nothing here reads the real HOME.
func feedbackTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BERTH_HOME", t.TempDir())
	t.Setenv(feedbackOffEnv, "")
	// gh is looked up on PATH; keep every test's answer independent of the
	// machine by defaulting to "no gh".
	t.Setenv("PATH", t.TempDir())
	resetFeedbackFlags()
}

func resetFeedbackFlags() {
	feedbackKindFlag = ""
	feedbackCommandFlag = ""
	feedbackErrorCodeFlag = ""
	feedbackObservationFlag = ""
	feedbackElapsedFlag = ""
	feedbackServicesFlag = -1
	feedbackNoteFlag = ""
	feedbackYesFlag = false
	feedbackDryRunFlag = false
	feedbackOpenFlag = false
	feedbackStatusJSON = false
	feedbackOnJSON = false
	feedbackOffJSON = false
	feedbackReportJSON = false
	feedbackLogJSON = false
}

func enableFeedback(t *testing.T) {
	t.Helper()
	record := feedback.Decide(feedback.DecisionEnabled, "v2.0.0", time.Now())
	if err := feedback.WriteConsent(paths.FeedbackPath(), record); err != nil {
		t.Fatalf("WriteConsent: %v", err)
	}
}

func TestFeedbackReportRefusesWithoutConsent(t *testing.T) {
	feedbackTestEnv(t)
	feedbackKindFlag, feedbackCommandFlag = "slow", "up"

	err := runFeedbackReport(testCommand(), nil)
	var coded *cliErr
	if !errors.As(err, &coded) || coded.Code != "invalid_config" {
		t.Fatalf("err = %v, want invalid_config", err)
	}
	if _, statErr := os.Stat(paths.FeedbackLedgerPath()); !os.IsNotExist(statErr) {
		t.Fatal("a refused report must not write the ledger")
	}
}

func TestFeedbackReportRespectsTheEnvironmentSwitch(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)
	t.Setenv(feedbackOffEnv, "1")
	feedbackKindFlag, feedbackCommandFlag = "slow", "up"

	err := runFeedbackReport(testCommand(), nil)
	var coded *cliErr
	if !errors.As(err, &coded) || coded.Code != "invalid_config" {
		t.Fatalf("err = %v, want invalid_config", err)
	}
}

func TestFeedbackReportDryRunPrintsAndSendsNothing(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)
	feedbackKindFlag, feedbackCommandFlag = "slow", "up"
	feedbackElapsedFlag, feedbackServicesFlag = "45s", 30
	feedbackDryRunFlag = true

	var err error
	out := captureStdout(t, func() { err = runFeedbackReport(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackReport: %v", err)
	}
	for _, want := range []string{
		"[feedback] slow · up · " + buildinfo.VersionValue(),
		"**Elapsed:** 30s-2m",
		"**Services:** 21+",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run is missing %q:\n%s", want, out)
		}
	}
	if _, statErr := os.Stat(paths.FeedbackLedgerPath()); !os.IsNotExist(statErr) {
		t.Fatal("a dry run must not write the ledger")
	}
}

func TestFeedbackReportFilesThroughGH(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)
	fakeGHSucceeds(t)
	feedbackKindFlag, feedbackCommandFlag = "stalled", "logs"

	var err error
	out := captureStdout(t, func() { err = runFeedbackReport(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackReport: %v", err)
	}
	if !strings.Contains(out, "filed https://github.com/") {
		t.Fatalf("output = %q, want a filed line", out)
	}
	entries, readErr := feedback.ReadLedger(paths.FeedbackLedgerPath())
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("ledger = (%+v, %v), want one entry", entries, readErr)
	}
	if entries[0].Channel != "gh" || entries[0].Kind != "stalled" || entries[0].Command != "logs" {
		t.Fatalf("entry = %+v", entries[0])
	}
}

func TestFeedbackReportFallsBackToTheIssuePage(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)
	feedbackKindFlag, feedbackCommandFlag = "slow", "up"

	var err error
	out := captureStdout(t, func() { err = runFeedbackReport(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackReport: %v", err)
	}
	if !strings.Contains(out, "https://github.com/"+feedback.DefaultRepo+"/issues/new?") {
		t.Fatalf("output = %q, want the prefilled issue URL", out)
	}
	entries, _ := feedback.ReadLedger(paths.FeedbackLedgerPath())
	if len(entries) != 1 || entries[0].Channel != "url" {
		t.Fatalf("ledger = %+v, want one url entry", entries)
	}
}

// A prefilled page is not a filed report. The JSON answer keeps the promise
// that "unknown or partial" is never spelled as success.
func TestFeedbackReportJSONDoesNotClaimSuccessForAURL(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)
	feedbackKindFlag, feedbackCommandFlag = "slow", "up"
	feedbackReportJSON = true

	var err error
	out := captureStdout(t, func() { err = runFeedbackReport(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackReport: %v", err)
	}
	var decoded struct {
		Sent    bool   `json:"sent"`
		Channel string `json:"channel"`
		Reason  string `json:"reason"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("output is not one JSON value: %v\n%s", err, out)
	}
	if decoded.Sent || decoded.Channel != "url" || decoded.Reason == "" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if !strings.HasPrefix(decoded.URL, "https://github.com/"+feedback.DefaultRepo+"/issues/new?") {
		t.Fatalf("url = %q", decoded.URL)
	}
}

func TestFeedbackReportNeedsConfirmationForANote(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)
	feedbackKindFlag, feedbackCommandFlag = "coarse", "status"
	feedbackNoteFlag = "only one line, not enough to decide"

	err := runFeedbackReport(testCommand(), nil)
	var usage usageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want a usage error without --yes on a pipe", err)
	}
	if !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("err = %q, want it to name the missing confirmation", err)
	}
	if _, statErr := os.Stat(paths.FeedbackLedgerPath()); !os.IsNotExist(statErr) {
		t.Fatal("an unconfirmed note must not be filed")
	}
}

func TestFeedbackReportRefusesASecretInTheNote(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)
	feedbackKindFlag, feedbackCommandFlag = "other", "status"
	feedbackNoteFlag = "the token ghp_" + strings.Repeat("a", 30) + " was rejected"
	feedbackYesFlag = true

	err := runFeedbackReport(testCommand(), nil)
	if err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("err = %v, want a credential refusal", err)
	}
	if _, statErr := os.Stat(paths.FeedbackLedgerPath()); !os.IsNotExist(statErr) {
		t.Fatal("a refused note must not be filed")
	}
}

func TestFeedbackReportSkipsADuplicate(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)
	report, buildErr := feedback.Build(feedback.Input{
		Kind: "slow", Command: "up", Version: buildinfo.VersionValue(),
		Platform: feedback.Platform(), Services: -1,
		Redactor: feedback.Redactor{},
	})
	if buildErr != nil {
		t.Fatalf("Build: %v", buildErr)
	}
	if err := feedback.AppendLedger(paths.FeedbackLedgerPath(), feedback.Entry{
		At:          time.Now().UTC().Format(time.RFC3339),
		Fingerprint: report.Fingerprint(),
		Kind:        "slow", Command: "up", Channel: "gh",
		URL: "https://github.com/SheathedSharp/option-berth/issues/1",
	}); err != nil {
		t.Fatalf("AppendLedger: %v", err)
	}
	feedbackKindFlag, feedbackCommandFlag = "slow", "up"

	var err error
	out := captureStdout(t, func() { err = runFeedbackReport(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackReport: %v", err)
	}
	if !strings.Contains(out, "the same report was filed") {
		t.Fatalf("output = %q, want the duplicate note", out)
	}
	entries, _ := feedback.ReadLedger(paths.FeedbackLedgerPath())
	if len(entries) != 1 {
		t.Fatalf("ledger grew to %d entries; a duplicate must not file", len(entries))
	}
}

// A refusal at the daily limit sends nothing and records nothing. The hint has
// to say both: the old wording ("nothing was sent; the local record is
// <path>") read as if the refused attempt had been written to the ledger.
func TestFeedbackReportDailyLimitRefusalRecordsNothing(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)
	now := time.Now().UTC()
	for i := 0; i < feedback.DailyLimit; i++ {
		if err := feedback.AppendLedger(paths.FeedbackLedgerPath(), feedback.Entry{
			At:          now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
			Fingerprint: fmt.Sprintf("filed-%d", i),
			Kind:        "slow", Command: "up", Channel: "gh",
			URL: fmt.Sprintf("https://github.com/SheathedSharp/option-berth/issues/%d", i+1),
		}); err != nil {
			t.Fatalf("AppendLedger: %v", err)
		}
	}
	feedbackKindFlag, feedbackCommandFlag = "misleading", "logs"

	err := runFeedbackReport(testCommand(), nil)
	var coded *cliErr
	if !errors.As(err, &coded) || coded.Code != "permission_denied" {
		t.Fatalf("err = %v, want permission_denied", err)
	}
	if !strings.Contains(coded.Hint, "nothing was sent and nothing was recorded") {
		t.Fatalf("hint = %q, want it to say neither sent nor recorded", coded.Hint)
	}
	if !strings.Contains(coded.Hint, paths.FeedbackLedgerPath()) {
		t.Fatalf("hint = %q, want the path of the existing records", coded.Hint)
	}
	entries, _ := feedback.ReadLedger(paths.FeedbackLedgerPath())
	if len(entries) != feedback.DailyLimit {
		t.Fatalf("ledger grew to %d entries; a refused report must not record", len(entries))
	}
}

func TestFeedbackStatusReportsTheDecision(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)

	var err error
	out := captureStdout(t, func() { err = runFeedbackStatus(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackStatus: %v", err)
	}
	if !strings.Contains(out, "decision   enabled") {
		t.Fatalf("output = %q", out)
	}
}

func TestFeedbackStatusJSONCarriesTheSwitch(t *testing.T) {
	feedbackTestEnv(t)
	enableFeedback(t)
	t.Setenv(feedbackOffEnv, "1")
	feedbackStatusJSON = true

	var err error
	out := captureStdout(t, func() { err = runFeedbackStatus(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackStatus: %v", err)
	}
	var decoded struct {
		Decision      string `json:"decision"`
		Enabled       bool   `json:"enabled"`
		DisabledByEnv bool   `json:"disabled_by_env"`
		Channel       string `json:"channel"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("status output is not one JSON value: %v\n%s", err, out)
	}
	if decoded.Decision != "enabled" || decoded.Enabled || !decoded.DisabledByEnv {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.Channel != "url" {
		t.Fatalf("channel = %q, want url without gh", decoded.Channel)
	}
}

func TestFeedbackOnAndOffWriteTheRecord(t *testing.T) {
	feedbackTestEnv(t)
	if err := runFeedbackOn(testCommand(), nil); err != nil {
		t.Fatalf("runFeedbackOn: %v", err)
	}
	consent, present, err := feedback.LoadConsent(paths.FeedbackPath())
	if err != nil || !present || !consent.Enabled() {
		t.Fatalf("after on: (%+v, %v, %v)", consent, present, err)
	}
	if err := runFeedbackOff(testCommand(), nil); err != nil {
		t.Fatalf("runFeedbackOff: %v", err)
	}
	consent, present, err = feedback.LoadConsent(paths.FeedbackPath())
	if err != nil || !present || consent.Enabled() {
		t.Fatalf("after off: (%+v, %v, %v)", consent, present, err)
	}
}

func TestFeedbackPromptStaysOffUnderTheEnvironmentSwitch(t *testing.T) {
	t.Setenv(feedbackOffEnv, "1")
	file, err := os.CreateTemp(t.TempDir(), "not-a-terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if feedbackPromptApplies(file, file) {
		t.Fatal("the question must not be asked when the switch is set")
	}
}

// fakeGHSucceeds puts an executable `gh` on PATH that answers auth and issue
// creation without the network, and records the issue body for assertions.
func fakeGHSucceeds(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  auth) exit 0 ;;\n" +
		"esac\n" +
		"/bin/cat >/dev/null\n" +
		"echo https://github.com/SheathedSharp/option-berth/issues/42\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// fakeGHForHistory puts a gh on PATH that answers the two reads the status and
// log surfaces make: who is signed in, and what happened to the report issues.
// It never touches the network.
func fakeGHForHistory(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1 $2\" in\n" +
		"  \"auth status\")\n" +
		"    printf '%s\\n' '{\"hosts\":{\"github.com\":[{\"state\":\"success\",\"active\":true,\"login\":\"zayJu\"}]}}'\n" +
		"    ;;\n" +
		"  \"issue list\")\n" +
		"    printf '%s\\n' '[{\"number\":42,\"state\":\"CLOSED\",\"stateReason\":\"COMPLETED\",\"title\":\"[feedback] slow · up · v2.2.0 · darwin/arm64\",\"url\":\"https://github.com/SheathedSharp/option-berth/issues/42\",\"updatedAt\":\"2026-11-13T01:00:00Z\",\"closedAt\":\"2026-11-13T01:00:00Z\",\"labels\":[]}]'\n" +
		"    ;;\n" +
		"  *)\n" +
		"    exit 1\n" +
		"    ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func appendFeedbackLedger(t *testing.T, entries ...feedback.Entry) {
	t.Helper()
	for _, entry := range entries {
		if err := feedback.AppendLedger(paths.FeedbackLedgerPath(), entry); err != nil {
			t.Fatalf("AppendLedger: %v", err)
		}
	}
}

// The account is part of the decision, so status names it before any report is
// composed instead of hiding it in the filing step.
func TestFeedbackStatusShowsTheGitHubAccountAndLocalTotals(t *testing.T) {
	feedbackTestEnv(t)
	fakeGHForHistory(t)
	appendFeedbackLedger(t,
		feedback.Entry{At: "2026-11-13T00:00:00Z", Kind: "slow", Command: "up", Channel: "gh", URL: "https://github.com/SheathedSharp/option-berth/issues/42"},
		feedback.Entry{At: "2026-11-12T00:00:00Z", Kind: "other", Command: "logs", Channel: "url", URL: "https://github.com/SheathedSharp/option-berth/issues/new?title=x"},
	)

	var err error
	out := captureStdout(t, func() { err = runFeedbackStatus(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackStatus: %v", err)
	}
	for _, want := range []string{
		"github     signed in as zayJu",
		"reports    2 recorded · 1 filed · 1 prefilled · last 2026-11-13",
		"`oberth feedback log` shows each issue's state",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status is missing %q:\n%s", want, out)
		}
	}

	feedbackStatusJSON = true
	jsonOut := captureStdout(t, func() { err = runFeedbackStatus(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackStatus --json: %v", err)
	}
	var doc struct {
		Channel string `json:"channel"`
		GitHub  struct {
			Available bool   `json:"available"`
			Account   string `json:"account"`
		} `json:"github"`
		Reports struct {
			Total     int `json:"total"`
			Filed     int `json:"filed"`
			Prefilled int `json:"prefilled"`
		} `json:"reports"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, jsonOut)
	}
	if doc.Channel != "gh" || !doc.GitHub.Available || doc.GitHub.Account != "zayJu" {
		t.Fatalf("github = %+v channel = %q", doc.GitHub, doc.Channel)
	}
	if doc.Reports.Total != 2 || doc.Reports.Filed != 1 || doc.Reports.Prefilled != 1 {
		t.Fatalf("reports = %+v", doc.Reports)
	}
}

func TestFeedbackStatusSaysWhenGHIsNotAvailable(t *testing.T) {
	feedbackTestEnv(t)

	var err error
	out := captureStdout(t, func() { err = runFeedbackStatus(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackStatus: %v", err)
	}
	if !strings.Contains(out, "github     not available (gh is not installed)") {
		t.Fatalf("status does not name the missing gh:\n%s", out)
	}
}

func TestFeedbackOnNamesTheAccountThatWillFile(t *testing.T) {
	feedbackTestEnv(t)
	fakeGHForHistory(t)

	var err error
	out := captureStdout(t, func() { err = runFeedbackOn(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackOn: %v", err)
	}
	if !strings.Contains(out, "github     signed in as zayJu; a filed report uses this account") {
		t.Fatalf("on does not name the account:\n%s", out)
	}

	feedbackOnJSON = true
	jsonOut := captureStdout(t, func() { err = runFeedbackOn(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackOn --json: %v", err)
	}
	var doc struct {
		GitHub struct {
			Account string `json:"account"`
		} `json:"github"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		t.Fatalf("on JSON: %v\n%s", err, jsonOut)
	}
	if doc.GitHub.Account != "zayJu" {
		t.Fatalf("github account = %q", doc.GitHub.Account)
	}
}

// The response half of the loop: one filed issue is resolved, one is not in the
// listing, and one never left the prefilled page. Each has to read differently.
func TestFeedbackLogShowsEachIssueOutcome(t *testing.T) {
	feedbackTestEnv(t)
	fakeGHForHistory(t)
	appendFeedbackLedger(t,
		feedback.Entry{At: "2026-11-13T00:00:00Z", Kind: "slow", Command: "up", Version: "v2.2.0", Channel: "gh", URL: "https://github.com/SheathedSharp/option-berth/issues/42"},
		feedback.Entry{At: "2026-11-12T00:00:00Z", Kind: "coarse", Command: "status", Version: "v2.2.0", Channel: "gh", URL: "https://github.com/SheathedSharp/option-berth/issues/43"},
		feedback.Entry{At: "2026-11-11T00:00:00Z", Kind: "other", Command: "logs", Version: "v2.2.0", Channel: "url", URL: "https://github.com/SheathedSharp/option-berth/issues/new?title=x"},
	)

	var err error
	out := captureStdout(t, func() { err = runFeedbackLog(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackLog: %v", err)
	}
	for _, want := range []string{
		"github     signed in as zayJu",
		"reports    3 recorded · 2 filed · 1 prefilled · last 2026-11-13",
		"issues     0 open · 1 resolved · 0 closed as not planned · 1 unknown",
		"#42",
		"resolved · closed 2026-11-13",
		"#43",
		"state unknown (the issue was not in the repository listing)",
		"prefilled page only; not confirmed filed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q:\n%s", want, out)
		}
	}

	feedbackLogJSON = true
	jsonOut := captureStdout(t, func() { err = runFeedbackLog(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackLog --json: %v", err)
	}
	var doc struct {
		GitHub  feedback.GHAccount     `json:"github"`
		Summary feedback.ReportSummary `json:"summary"`
		Entries []struct {
			Kind   string          `json:"kind"`
			Status string          `json:"status"`
			Reason string          `json:"reason"`
			Issue  *feedback.Issue `json:"issue"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		t.Fatalf("log JSON: %v\n%s", err, jsonOut)
	}
	if doc.GitHub.Account != "zayJu" {
		t.Fatalf("github = %+v", doc.GitHub)
	}
	if doc.Summary.Total != 3 || doc.Summary.Filed != 2 || doc.Summary.Prefilled != 1 ||
		doc.Summary.Resolved != 1 || doc.Summary.Unknown != 1 {
		t.Fatalf("summary = %+v", doc.Summary)
	}
	if len(doc.Entries) != 3 {
		t.Fatalf("entries = %d", len(doc.Entries))
	}
	if doc.Entries[0].Status != feedback.OutcomeResolved || doc.Entries[0].Issue == nil ||
		doc.Entries[0].Issue.Number != 42 || doc.Entries[0].Issue.Title == "" {
		t.Fatalf("first entry = %+v", doc.Entries[0])
	}
	if doc.Entries[1].Status != feedback.OutcomeUnknown || doc.Entries[1].Reason == "" {
		t.Fatalf("second entry = %+v", doc.Entries[1])
	}
	if doc.Entries[2].Status != feedback.OutcomeNotFiled {
		t.Fatalf("third entry = %+v", doc.Entries[2])
	}
}

// Without gh the local record is still the answer; it just says why the issue
// state is unknown instead of inventing one.
func TestFeedbackLogWithoutGHKeepsTheLocalRecord(t *testing.T) {
	feedbackTestEnv(t)
	appendFeedbackLedger(t,
		feedback.Entry{At: "2026-11-13T00:00:00Z", Kind: "slow", Command: "up", Version: "v2.2.0", Channel: "gh", URL: "https://github.com/SheathedSharp/option-berth/issues/42"},
	)

	var err error
	out := captureStdout(t, func() { err = runFeedbackLog(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackLog: %v", err)
	}
	for _, want := range []string{
		"github     not available (gh is not installed)",
		"issues     0 open · 0 resolved · 0 closed as not planned · 1 unknown",
		"state unknown (gh is not installed)",
		"https://github.com/SheathedSharp/option-berth/issues/42",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q:\n%s", want, out)
		}
	}

	feedbackLogJSON = true
	jsonOut := captureStdout(t, func() { err = runFeedbackLog(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runFeedbackLog --json: %v", err)
	}
	var doc struct {
		IssueReason string `json:"issue_reason"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		t.Fatalf("log JSON: %v\n%s", err, jsonOut)
	}
	if doc.IssueReason != "gh is not installed" {
		t.Fatalf("issue_reason = %q", doc.IssueReason)
	}
}
