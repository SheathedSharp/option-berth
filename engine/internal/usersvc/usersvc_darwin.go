//go:build darwin

package usersvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Label is the launchd job label. It is the reverse-DNS form of the product's
// own domain, so a person reading `launchctl list` can tell where the job came
// from without opening it.
const Label = "com.sheathedsharp.option-berth.runtime"

// current is the macOS supervisor.
var current Supervisor = launchd{}

type launchd struct{}

func (launchd) Name() string { return "launchd" }

// UnitPath is the per-user LaunchAgent install.sh writes. launchd reads that
// directory at login, so a machine without this file has no persistent runtime
// service and the CLI keeps starting one with `run --detach`.
func (launchd) UnitPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

func (l launchd) Installed() bool {
	path := l.UnitPath()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// Active asks launchd whether the job is loaded. `print` is the query, and a
// non-zero exit is how launchd says the job is not there: an answer, not an
// error to report.
func (launchd) Active(ctx context.Context) (bool, error) {
	for _, domain := range domains() {
		if err := run(ctx, "launchctl", "print", job(domain)); err == nil {
			return true, nil
		}
	}
	return false, nil
}

// Start runs a job that is loaded but not running: `kickstart` without -k never
// kills a live process, so a runtime service that is already up is left alone.
func (launchd) Start(ctx context.Context) error {
	return domains().firstAccepting(func(domain string) error {
		return run(ctx, "launchctl", "kickstart", job(domain))
	})
}

// Restart replaces the running process: -k kills what is there first, which is
// the only way to move a runtime service onto a newer build.
func (launchd) Restart(ctx context.Context) error {
	return domains().firstAccepting(func(domain string) error {
		return run(ctx, "launchctl", "kickstart", "-k", job(domain))
	})
}

// domains lists the launchd domains this job may live in. A LaunchAgent
// bootstrapped from a login session is in gui/<uid>; one bootstrapped from a
// context without a GUI session — an SSH login, a CI runner — lands in
// user/<uid>. Both name the same per-user job, and the caller cannot tell which
// one took, so every command tries them in that order.
func domains() domainList {
	uid := os.Getuid()
	return domainList{fmt.Sprintf("gui/%d", uid), fmt.Sprintf("user/%d", uid)}
}

// job is the fully qualified target launchctl takes.
func job(domain string) string { return domain + "/" + Label }

// domainList tries each domain in turn.
type domainList []string

// firstAccepting runs action against the first domain that accepts it and
// returns the last failure when none does, so the error names the domain that
// was tried last rather than the one that was only a fallback.
func (d domainList) firstAccepting(action func(domain string) error) error {
	var last error
	for _, domain := range d {
		if last = action(domain); last == nil {
			return nil
		}
	}
	return last
}
