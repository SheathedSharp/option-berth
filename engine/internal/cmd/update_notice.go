package cmd

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/versioncheck"
	"github.com/spf13/cobra"
)

// noticeProbe is the release check a command starts before it does its work
// and reads when it has finished. Running it alongside the command means a
// short command pays at most the leftover of the budget, and a command that
// takes longer than the budget pays nothing.
type noticeProbe struct {
	result chan versioncheck.State
	start  time.Time
}

var updateProbe *noticeProbe

// startUpdateProbe launches the terminal footer's release check for a command
// that will write human output to a terminal. It reuses the cached answer, so
// the network is touched only when the cache is stale, and it never runs for
// JSON, --help, the welcome screen or an explicit `version --check` — each of
// those either has its own answer or must stay a clean machine document.
func startUpdateProbe(cmd *cobra.Command) {
	if updateProbe != nil || !noticeEligible(cmd) {
		return
	}
	current := buildinfo.VersionValue()
	if !versioncheck.IsRelease(current) || !versioncheck.EnabledFile(os.Stderr) {
		return
	}
	probe := &noticeProbe{result: make(chan versioncheck.State, 1), start: time.Now()}
	updateProbe = probe
	go func() {
		state, _ := versioncheck.Lookup(context.Background(), current, false, versioncheck.WelcomeTimeout)
		probe.result <- state
	}()
}

// noticeEligible says whether this invocation may end with an update footer.
// The welcome screen prints the fuller upgrade block itself, and an explicit
// `version --check` has already answered the question.
func noticeEligible(cmd *cobra.Command) bool {
	if versionFlag || jsonMode {
		return false
	}
	if cmd == rootCmd || (cmd == versionCmd && versionCheckFlag) {
		return false
	}
	if help, err := cmd.Flags().GetBool("help"); err == nil && help {
		return false
	}
	return true
}

// printUpdateNotice waits for the probe the command started, within what is
// left of the same budget the welcome screen allows, and prints the footer when
// a newer release is known. A slow or unreachable feed stays silent: the
// command's own result has already been written, and a notice is never worth
// holding the terminal for.
func printUpdateNotice() {
	probe := updateProbe
	updateProbe = nil
	if probe == nil {
		return
	}
	state, ok := awaitUpdateNotice(probe)
	if !ok {
		return
	}
	notice := updateNotice(state)
	if notice == "" {
		return
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, notice)
	versioncheck.MarkNotified(state.Current, state.Latest)
}

// awaitUpdateNotice reads the probe's result, waiting only for what is left of
// the welcome screen's budget. An answer that is not ready by then is dropped,
// not cancelled: cancelling would record the cancellation as a failed check and
// silence the feed for the whole failure TTL.
func awaitUpdateNotice(probe *noticeProbe) (versioncheck.State, bool) {
	select {
	case state := <-probe.result:
		return state, true
	default:
		remaining := versioncheck.WelcomeTimeout - time.Since(probe.start)
		if remaining <= 0 {
			return versioncheck.State{}, false
		}
		select {
		case state := <-probe.result:
			return state, true
		case <-time.After(remaining):
			return versioncheck.State{}, false
		}
	}
}

// updateNotice is the two lines a terminal command ends with when this build
// is behind: what is newer, and the exact commands that make the running
// installation newer. It stays quiet for an unknown answer — "not checked"
// never becomes "update available" — and for a release already announced.
func updateNotice(state versioncheck.State) string {
	if !state.UpdateAvailable() || state.Notified == state.Latest {
		return ""
	}
	return fmt.Sprintf("%s %s → %s\n%s %s, then %s",
		display.Yellow("A new release of oberth is available:"),
		display.Dim(state.Current), display.Bold(state.Latest),
		display.Dim("To upgrade, run:"), display.Bold(upgradeCommand(runtime.GOOS)),
		display.Bold("oberth runtime service stop"))
}
