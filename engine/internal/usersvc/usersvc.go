// Package usersvc is the user-level supervisor that owns the runtime service.
//
// The runtime service used to be started ad hoc: `oberth runtime service run
// --detach` forked a child, and that child was reaped for being idle or died
// with the login session. "The runtime service is running" was therefore a fact
// about recent activity, not about the machine. This package makes it a fact
// about the machine: install.sh registers one user-level unit — a launchd
// LaunchAgent on macOS, a systemd user unit on Linux — and the supervisor keeps
// exactly one runtime service alive, so a client never has to start one itself.
//
// Windows has no such unit here. The installer registers nothing, `Current()`
// reports itself uninstalled, and the runtime service there is still started
// with `run --detach`.
//
// The supervisor is run through PATH rather than by absolute location, for two
// reasons: launchctl and systemctl live somewhere different on every platform
// generation and are not always present at all, and a test can put a fake one
// first on PATH and assert the exact argv without a real supervisor anywhere
// near the machine.
package usersvc

import "context"

// Supervisor is one platform's user-level service manager.
type Supervisor interface {
	// Name identifies the supervisor in messages: "launchd", "systemd" or "none".
	Name() string
	// UnitPath is the file that registers the runtime service: the plist on macOS,
	// the unit file on Linux, and "" where the platform has no user-level unit.
	UnitPath() string
	// Installed reports whether that file exists. It is the one fact that says this
	// machine's runtime service belongs to a supervisor rather than to whoever ran
	// `run --detach`.
	Installed() bool
	// Active reports whether the supervisor is running the unit right now. An
	// installed unit that is not loaded is inactive, which is an answer rather
	// than an error.
	Active(ctx context.Context) (bool, error)
	// Start asks the supervisor to run the unit. It never writes the unit file:
	// installing it is install.sh's job, and starting must not resurrect one the
	// user removed.
	Start(ctx context.Context) error
	// Restart asks the supervisor to replace the running process with a new one.
	Restart(ctx context.Context) error
}

// Current returns this platform's supervisor.
func Current() Supervisor { return current }
