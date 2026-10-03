// Package userenv recovers the environment a daemon started from the desktop is
// missing.
//
// On macOS a process started from the Dock — the app, and every process it
// spawns in turn — inherits launchd's PATH and nothing else:
// /usr/bin:/bin:/usr/sbin:/sbin. Homebrew, nvm, pyenv, uv and each project's own
// toolchain live outside those four directories.
//
// That matters here more than it would elsewhere, because of what this daemon
// is for: it starts the user's services. A service's environment is the
// daemon's (see the daemon's group start path), so `pnpm run dev` or
// `docker compose up` came back "command not found" in the one program whose
// job is to start it — and "open a terminal and run it from there instead" is
// exactly the answer this tool exists to make unnecessary.
//
// So the daemon asks the login shell what PATH it would have, once, and keeps
// the answer for everything it starts afterwards.
package userenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Timeout bounds the login shell. A profile that hangs must not hang the daemon
// with it; the daemon comes up with whatever PATH it already had.
const Timeout = 3 * time.Second

// marker fences the answer off from anything a shell profile prints on the way
// (`echo "welcome back"` in a .zprofile is common enough).
const marker = "__berth_path__"

// LoginPath returns the PATH the user's login shell would export.
//
// It is interactive (`-i`) on purpose: `.zshrc` is read by interactive shells
// only, and `.zshrc` is where most people put PATH. A non-interactive
// `zsh -lc` sees `.zprofile` alone and comes back with the system default —
// which is the very answer this function exists to improve on.
func LoginPath(timeout time.Duration) (string, bool) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	// A shell that is not there is not an error worth reporting: the caller
	// keeps the PATH it has.
	if _, err := os.Stat(shell); err != nil {
		return "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-ilc",
		"printf '"+marker+"%s"+marker+"' \"$PATH\"")
	// A profile's own output goes nowhere: only stdout is read, and the marker
	// says which part of it is the answer.
	cmd.Stderr = nil
	// The timeout alone does not end the wait. Killing the shell leaves
	// whatever it forked — a `sleep` in a profile, a prompt hook — holding the
	// stdout pipe, and Output() waits for that pipe to close, not for the
	// process. Without this a slow profile turned the 3-second bound into
	// however long the profile's last child cared to live (measured: a shell
	// that slept 30 s made LoginPath return in 30 s).
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}

	path, ok := between(string(out), marker)
	if !ok {
		return "", false
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	return path, true
}

// between returns what sits between the first two markers.
func between(s, marker string) (string, bool) {
	i := strings.Index(s, marker)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(marker):]
	j := strings.Index(rest, marker)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// Merge puts the login shell's entries first and appends whatever the current
// PATH adds that is not already there, so a daemon launched from a terminal
// (whose PATH is already right) keeps its own ordering and loses nothing.
func Merge(login, current string) string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range [2]string{login, current} {
		for _, dir := range strings.Split(list, ":") {
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return strings.Join(out, ":")
}

// Ensure replaces this process's PATH with Merge(login shell, current) and
// reports the result plus whether it changed anything.
//
// It is called once, by the process that becomes the daemon. Everything the
// daemon starts afterwards — a service, its children, anything a service shells
// out to — inherits the answer, so this is the one place the toolchain has to be
// got right.
func Ensure(timeout time.Duration) (string, bool) {
	login, ok := LoginPath(timeout)
	if !ok {
		return os.Getenv("PATH"), false
	}
	current := os.Getenv("PATH")
	merged := Merge(login, current)
	if merged == current {
		return merged, false
	}
	if err := os.Setenv("PATH", merged); err != nil {
		return current, false
	}
	return merged, true
}
