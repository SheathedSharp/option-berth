package cmd

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/versioncheck"
	"github.com/spf13/cobra"
)

func TestUpdateNoticeNamesTheReleaseAndTheUpgrade(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	notice := updateNotice(versioncheck.State{Current: "v1.4.0", Latest: "v2.0.0"})
	for _, want := range []string{
		"A new release of oberth is available:",
		"v1.4.0",
		"v2.0.0",
		upgradeCommand(runtime.GOOS),
		"oberth runtime service stop",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice = %q, want it to name %q", notice, want)
		}
	}
	if strings.Contains(notice, "\x1b[") {
		t.Errorf("notice = %q, want no ANSI under NO_COLOR", notice)
	}
}

// A notice is a fact, not a nag: same version, a development build, a failed
// check and an already announced release all stay quiet.
func TestUpdateNoticeStaysQuiet(t *testing.T) {
	for _, test := range []struct {
		name  string
		state versioncheck.State
	}{
		{"same release", versioncheck.State{Current: "v2.0.0", Latest: "v2.0.0"}},
		{"newer build", versioncheck.State{Current: "v2.1.0", Latest: "v2.0.0"}},
		{"development build", versioncheck.State{Current: "dev", Latest: "v2.0.0"}},
		{"failed check", versioncheck.State{Current: "v1.4.0", Error: "network down"}},
		{"already announced", versioncheck.State{Current: "v1.4.0", Latest: "v2.0.0", Notified: "v2.0.0"}},
		{"empty", versioncheck.State{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if notice := updateNotice(test.state); notice != "" {
				t.Fatalf("notice = %q, want nothing", notice)
			}
		})
	}
}

func TestAwaitUpdateNoticeTakesAReadyAnswer(t *testing.T) {
	probe := &noticeProbe{result: make(chan versioncheck.State, 1), start: time.Now()}
	want := versioncheck.State{Current: "v1.4.0", Latest: "v2.0.0"}
	probe.result <- want

	got, ok := awaitUpdateNotice(probe)
	if !ok || got != want {
		t.Fatalf("awaitUpdateNotice = %+v, %v, want the ready answer", got, ok)
	}
}

func TestAwaitUpdateNoticeDropsAnAnswerPastTheBudget(t *testing.T) {
	probe := &noticeProbe{
		result: make(chan versioncheck.State, 1),
		start:  time.Now().Add(-2 * versioncheck.WelcomeTimeout),
	}
	if _, ok := awaitUpdateNotice(probe); ok {
		t.Fatal("awaitUpdateNotice waited past the budget for an answer")
	}
}

func TestNoticeEligibleSkipsMachineAndExplicitAnswers(t *testing.T) {
	previousJSON, previousVersion, previousCheck := jsonMode, versionFlag, versionCheckFlag
	jsonMode, versionFlag, versionCheckFlag = false, false, false
	t.Cleanup(func() {
		jsonMode, versionFlag, versionCheckFlag = previousJSON, previousVersion, previousCheck
	})

	sub := &cobra.Command{Use: "status"}
	if !noticeEligible(sub) {
		t.Fatal("a human subcommand is not eligible for the notice")
	}
	if noticeEligible(rootCmd) {
		t.Fatal("the welcome screen already prints its own upgrade block")
	}

	jsonMode = true
	if noticeEligible(sub) {
		t.Fatal("a --json command must not write a terminal footer")
	}
	jsonMode = false

	versionCheckFlag = true
	if noticeEligible(versionCmd) {
		t.Fatal("version --check already answered the question")
	}
	versionCheckFlag = false

	versionFlag = true
	if noticeEligible(sub) {
		t.Fatal("--version must stay the short shell-facing probe")
	}
	versionFlag = false

	help := &cobra.Command{Use: "helpful"}
	help.Flags().Bool("help", true, "")
	if noticeEligible(help) {
		t.Fatal("a help screen must not grow an update footer")
	}
}
