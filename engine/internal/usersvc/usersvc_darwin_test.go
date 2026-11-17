//go:build darwin

package usersvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guiJob and userJob are the two launchd targets every command may be sent to.
func guiJob() string  { return fmt.Sprintf("gui/%d/%s", os.Getuid(), Label) }
func userJob() string { return fmt.Sprintf("user/%d/%s", os.Getuid(), Label) }

// The plist's existence is the whole meaning of "this machine's runtime service
// belongs to launchd": install.sh writes it, uninstall.sh removes it, and every
// command that would otherwise fork a detached child asks this first.
func TestInstalledFollowsThePlistFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	supervisor := Current()
	if supervisor.Installed() {
		t.Fatal("Installed() = true before any plist was written")
	}
	want := filepath.Join(home, "Library", "LaunchAgents", "com.sheathedsharp.option-berth.runtime.plist")
	if got := supervisor.UnitPath(); got != want {
		t.Fatalf("UnitPath() = %q, want %q", got, want)
	}
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !supervisor.Installed() {
		t.Fatal("Installed() = false with the plist on disk")
	}
}

// A job bootstrapped from a GUI login session is in gui/<uid>, so that is what a
// start must ask for — and nothing else.
func TestStartKickstartsTheGuiDomain(t *testing.T) {
	log := shim(t, "launchctl", "exit 0")
	if err := Current().Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	wantCalls(t, log, "launchctl kickstart "+guiJob())
}

// Over SSH there is no GUI session, and launchd puts the same job in user/<uid>.
// The first domain's refusal is not the answer; the second one is.
func TestStartFallsBackToTheUserDomain(t *testing.T) {
	log := shim(t, "launchctl", "case \"$*\" in\n*gui/*) exit 1 ;;\nesac\nexit 0")
	if err := Current().Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	wantCalls(t, log, "launchctl kickstart "+guiJob(), "launchctl kickstart "+userJob())
}

// When neither domain takes the job the error has to name the command that was
// tried and what launchctl said about it, because that text is the only
// diagnosis a user gets.
func TestStartReportsTheSupervisorFailure(t *testing.T) {
	shim(t, "launchctl", "echo 'Could not find service' >&2\nexit 113")
	err := Current().Start(context.Background())
	if err == nil {
		t.Fatal("Start() = nil with a launchctl that refuses every domain")
	}
	for _, want := range []string{"launchctl kickstart", userJob(), "Could not find service"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// Restarting to pick up a new build has to replace the process, not adopt it:
// kickstart -k is the only form that kills what is running.
func TestRestartReplacesTheRunningJob(t *testing.T) {
	log := shim(t, "launchctl", "exit 0")
	if err := Current().Restart(context.Background()); err != nil {
		t.Fatalf("Restart() = %v, want nil", err)
	}
	wantCalls(t, log, "launchctl kickstart -k "+guiJob())
}

// Active is a query: `print` succeeding means the job is loaded, and every
// domain refusing means it is not — never an error to report.
func TestActiveAsksLaunchdAboutBothDomains(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		body  string
		state bool
		calls []string
	}{
		{"loaded in the gui domain", "exit 0", true, []string{"launchctl print " + guiJob()}},
		{"loaded in the user domain", "case \"$*\" in\n*gui/*) exit 1 ;;\nesac\nexit 0", true,
			[]string{"launchctl print " + guiJob(), "launchctl print " + userJob()}},
		{"not loaded", "exit 113", false, []string{"launchctl print " + guiJob(), "launchctl print " + userJob()}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			log := shim(t, "launchctl", testCase.body)
			active, err := Current().Active(context.Background())
			if err != nil {
				t.Fatalf("Active() error = %v, want nil", err)
			}
			if active != testCase.state {
				t.Fatalf("Active() = %v, want %v", active, testCase.state)
			}
			wantCalls(t, log, testCase.calls...)
		})
	}
}

// A machine without launchctl on PATH is a real case (a stripped container, a CI
// runner). The failure must say which command could not be run rather than
// looking like a service that refused to start.
func TestStartSaysWhenLaunchctlIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := Current().Start(context.Background())
	if err == nil {
		t.Fatal("Start() = nil without launchctl on PATH")
	}
	if !strings.Contains(err.Error(), "launchctl kickstart") {
		t.Errorf("error = %v, want it to name the command that could not run", err)
	}
}
