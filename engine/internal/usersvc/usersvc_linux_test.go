//go:build linux

package usersvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The unit file's existence is the whole meaning of "this machine's runtime
// service belongs to systemd": install.sh writes it, uninstall.sh removes it,
// and every command that would otherwise fork a detached child asks this first.
func TestInstalledFollowsTheUnitFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	supervisor := Current()
	if supervisor.Installed() {
		t.Fatal("Installed() = true before any unit was written")
	}
	want := filepath.Join(home, ".config", "systemd", "user", "option-berth-runtime.service")
	if got := supervisor.UnitPath(); got != want {
		t.Fatalf("UnitPath() = %q, want %q", got, want)
	}
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !supervisor.Installed() {
		t.Fatal("Installed() = false with the unit file on disk")
	}
}

// $XDG_CONFIG_HOME moves the unit, and the resolver has to follow it: systemd
// --user reads the variable, so a file written anywhere else is never loaded.
func TestUnitPathFollowsXDGConfigHome(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	want := filepath.Join(config, "systemd", "user", "option-berth-runtime.service")
	if got := Current().UnitPath(); got != want {
		t.Fatalf("UnitPath() = %q, want %q", got, want)
	}
}

// Every call is a --user call: the unit belongs to the person, and a
// machine-wide systemctl would need root and reach a service that is not theirs.
func TestCommandsAreUserScoped(t *testing.T) {
	for _, testCase := range []struct {
		name string
		call func(context.Context) error
		argv string
	}{
		{"start", Current().Start, "systemctl --user start option-berth-runtime.service"},
		{"restart", Current().Restart, "systemctl --user restart option-berth-runtime.service"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			log := shim(t, "systemctl", "exit 0")
			if err := testCase.call(context.Background()); err != nil {
				t.Fatalf("%s error = %v, want nil", testCase.name, err)
			}
			wantCalls(t, log, testCase.argv)
		})
	}
}

// Active answers from systemctl's exit status alone, in both directions.
func TestActiveFollowsIsActive(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		body   string
		active bool
	}{
		{"running", "exit 0", true},
		{"not running", "exit 3", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			log := shim(t, "systemctl", testCase.body)
			active, err := Current().Active(context.Background())
			if err != nil {
				t.Fatalf("Active() error = %v, want nil", err)
			}
			if active != testCase.active {
				t.Fatalf("Active() = %v, want %v", active, testCase.active)
			}
			wantCalls(t, log, "systemctl --user is-active --quiet option-berth-runtime.service")
		})
	}
}

// A machine without systemctl on PATH is a real case (a container without
// systemd). The failure must say which command could not be run rather than
// looking like a unit that refused to start.
func TestStartSaysWhenSystemctlIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := Current().Start(context.Background())
	if err == nil {
		t.Fatal("Start() = nil without systemctl on PATH")
	}
	if !strings.Contains(err.Error(), "systemctl --user start") {
		t.Errorf("error = %v, want it to name the command that could not run", err)
	}
}
