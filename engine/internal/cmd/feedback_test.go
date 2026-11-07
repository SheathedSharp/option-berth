package cmd

import (
	"encoding/json"
	"errors"
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
