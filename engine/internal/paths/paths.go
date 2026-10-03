// Package paths is the one place that knows where option-berth keeps its files.
//
// Before this package existed the layout was spelled out eight times — config,
// db, logs, profiles, runs, credentials, the share install id and the daemon's
// socket each built `~/.option-berth/...` for themselves, with comments
// explaining that they mirrored internal/config "to avoid an import cycle".
// Copying a path eight times is how a codebase ends up with a state directory
// that is named one thing in the docs and another on disk.
//
// It is a leaf: os, filepath and runtime, nothing else, so anything may import it.
//
// # One directory, and why not ~/.config
//
// Everything lives in ~/.option-berth: config.yaml, the database, the socket, the
// lock, logs, profiles, the runs registry and credentials.
//
// The obvious alternative is `~/.config`, which is where the upstream project
// put it. It is the wrong shape for this: **a SQLite database, a unix
// socket, a lock file and rotated logs are state, not configuration.** The
// freedesktop spec that names ~/.config says state goes in ~/.local/state, cache
// in ~/.cache and sockets in $XDG_RUNTIME_DIR — upstream bundled all of it into
// the config directory, which is the one thing the spec does not say.
//
// So the choice was between doing XDG properly (three directories to look in)
// and owning exactly one directory. This program's whole point is being easy to
// inspect when something is wrong, and "everything is in ~/.option-berth" is a
// sentence you can act on. It also matches where the rest of the toolchain on a
// developer's machine keeps itself (~/.codex, ~/.claude, ~/.cc-switch).
//
// $XDG_RUNTIME_DIR is still honoured for the socket alone, because on Linux that
// is what it is for and it buys per-boot cleanup. macOS does not set it, so there
// the socket sits in the directory like everything else.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// File names inside Dir. Each is exported as a constant so a caller that needs
// to recognise a file (doctor, the corruption path) does not retype the string.
const (
	DirName       = ".option-berth"
	ConfigFile    = "config.yaml"
	DBFile        = "option-berth.db"
	SocketFile    = "daemon.sock"
	LockFile      = "daemon.lock"
	LogFile       = "daemon.log"
	RunsFile      = "runs.json"
	Credentials   = "credentials.json"
	InstallIDFile = "install_id"
	// JevConfigFile is the owner-only local configuration written by the
	// macOS settings page. It is deliberately separate from config.yaml:
	// that file is a general, human-readable settings map and must never
	// become a place where an API key can be printed by config commands.
	JevConfigFile = "jev.json"
	// LegacyJevKeyFile is read-only compatibility for the pre-settings adapter.
	// A new settings file takes precedence and can explicitly disable Jev.
	LegacyJevKeyFile = "decide.key"

	LogsDir     = "logs"
	DraftsDir   = "drafts"
	ProfilesDir = "profiles"
	// SeenCLIFile is the `status` command's ledger of what it last showed: the
	// anchor its "和上次比" is measured from. The app keeps one of its own
	// (seen.json, written by the Swift client) — two readers, two anchors.
	SeenCLIFile = "seen-cli.json"
)

// Environment variables that override what this package resolves. All three are
// checked here and nowhere else.
const (
	// HomeEnv relocates the whole directory — the switch a test harness, a CI
	// job or a second instance uses. It names the directory itself, the way
	// $CARGO_HOME does: BERTH_HOME=/tmp/x means files land in /tmp/x, not
	// /tmp/x/.option-berth.
	HomeEnv = "BERTH_HOME"
	// SocketEnv points the daemon somewhere specific, for a socket on a tmpfs
	// or a caller that already knows where to look.
	SocketEnv = "BERTH_SOCKET"
	// DBEnv points the database somewhere specific.
	DBEnv = "BERTH_DB"
	// LogDirEnv relocates the per-service logs `oberth start` writes.
	LogDirEnv = "BERTH_LOG_DIR"
)

// WindowsPipe is the named pipe the daemon listens on under Windows. A pipe has
// no directory of its own, so the socket-related helpers below have to special
// case it.
const WindowsPipe = `\\.\pipe\option-berth`

// Dir is the directory option-berth owns: $BERTH_HOME if set, else ~/.option-berth.
//
// A home that cannot be resolved falls back to the working directory rather than
// to the filesystem root: the daemon should start, not create /.option-berth.
func Dir() string {
	return dirFrom(os.Getenv, os.UserHomeDir)
}

func dirFrom(getenv func(string) string, home func() (string, error)) string {
	if override := strings.TrimSpace(getenv(HomeEnv)); override != "" {
		return override
	}
	resolved, err := home()
	if err != nil || resolved == "" {
		return DirName
	}
	return filepath.Join(resolved, DirName)
}

// EnsureDir creates Dir with 0700 and returns it. Only this program's own
// directory is created, never a directory a caller pointed us at.
func EnsureDir() (string, error) {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// Socket is the address the daemon listens on.
//
//	$BERTH_SOCKET                       if set and non-empty (all platforms)
//	\\.\pipe\option-berth                     on Windows
//	$XDG_RUNTIME_DIR/option-berth/daemon.sock if that variable is set
//	~/.option-berth/daemon.sock               otherwise
//
// `oberth daemon path` prints the result, so no client has to hardcode it.
func Socket() string {
	return SocketFrom(os.Getenv, os.UserHomeDir, runtime.GOOS)
}

// SocketFrom is Socket with its environment injected, so the whole env matrix
// can be tested on one host.
func SocketFrom(getenv func(string) string, home func() (string, error), goos string) string {
	if override := getenv(SocketEnv); override != "" {
		return override
	}
	if goos == "windows" {
		return WindowsPipe
	}
	if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "option-berth", SocketFile)
	}
	return filepath.Join(dirFrom(getenv, home), SocketFile)
}

// SocketWithoutEnv is Socket with $BERTH_SOCKET hidden, i.e. where the daemon
// would listen if the caller had not pointed it elsewhere. Ownership rules and
// `doctor` need that distinction.
func SocketWithoutEnv() string {
	return SocketFrom(without(os.Getenv, SocketEnv), os.UserHomeDir, runtime.GOOS)
}

// OwnsSocketDir reports whether the directory holding path is one option-berth
// makes for itself, rather than one a caller pointed $BERTH_SOCKET at. Only a
// directory we own may be tightened to 0700: `BERTH_SOCKET=/tmp/x.sock` must not
// chmod /tmp.
func OwnsSocketDir(path string) bool {
	return OwnsSocketDirFrom(os.Getenv, os.UserHomeDir, runtime.GOOS, path)
}

// OwnsSocketDirFrom is OwnsSocketDir with its environment injected, so the rule
// can be tested across the whole env matrix on one host.
func OwnsSocketDirFrom(getenv func(string) string, home func() (string, error), goos string, path string) bool {
	def := SocketFrom(without(getenv, SocketEnv), home, goos)
	return filepath.Clean(filepath.Dir(path)) == filepath.Clean(filepath.Dir(def))
}

// Lock is the single-instance lock. It sits beside the socket, so a socket on a
// per-boot tmpfs takes its lock with it; a named pipe, which has no directory,
// falls back to Dir.
func Lock(socket string) string {
	if strings.HasPrefix(socket, `\\`) || filepath.Dir(socket) == "." {
		return filepath.Join(Dir(), LockFile)
	}
	return filepath.Join(filepath.Dir(socket), LockFile)
}

// DB is the default database location. $BERTH_DB overrides it.
func DB() string {
	if override := strings.TrimSpace(os.Getenv(DBEnv)); override != "" {
		return override
	}
	return filepath.Join(Dir(), DBFile)
}

// ConfigPath is config.yaml inside Dir.
func ConfigPath() string { return filepath.Join(Dir(), ConfigFile) }

// JevConfigPath is the local Jev settings file. The file is owned by the
// optional agent-side adapter and the macOS settings page, not by the daemon.
func JevConfigPath() string { return filepath.Join(Dir(), JevConfigFile) }

// LegacyJevKeyPath is retained so an existing adapter installation keeps
// working until the user saves the new settings page once.
func LegacyJevKeyPath() string { return filepath.Join(Dir(), LegacyJevKeyFile) }

// LogPath is the daemon's rotated log file.
func LogPath() string { return filepath.Join(Dir(), LogFile) }

// Logs is the directory of per-service logs that `oberth start` writes.
// $BERTH_LOG_DIR overrides it.
func Logs() string {
	if override := strings.TrimSpace(os.Getenv(LogDirEnv)); override != "" {
		return override
	}
	return filepath.Join(Dir(), LogsDir)
}

// Drafts is the directory holding the drafts of a project's oberth.yaml
// that a local agent wrote (init draft). Nothing reads it on its own: a
// draft becomes a manifest only through `oberth init adopt`,
// which is a person's decision — the draft itself declares nothing.
func Drafts() string { return filepath.Join(Dir(), DraftsDir) }

// Draft is where the draft for one project's group lives. It is keyed by the
// group name init would give the project (directory or worktree), the same
// key `init adopt` resolves.
func Draft(group string) string { return filepath.Join(Drafts(), group+".yaml") }

// Profiles is the directory of saved launch profiles.
func Profiles() string { return filepath.Join(Dir(), ProfilesDir) }

// SeenCLI is the `status` command's ledger of the last thing it showed — the
// anchor `oberth status` diffs against.
//
// It is a second file, not a second key in the app's seen.json, and the reason
// is what "上次" means on each side: the app rolls its anchor every few seconds
// while a face is open (it means "the last thing *you* looked at"), so an agent
// that marked the project before it started working would find its anchor
// overwritten by a window somebody had open. Same shape, two readers, two
// anchors — and because that is invisible, both files are documented where a
// reader would look for the other one.
func SeenCLI() string { return filepath.Join(Dir(), SeenCLIFile) }

// Runs is the run registry that lets a restarted CLI find what a previous run
// started.
func Runs() string { return filepath.Join(Dir(), RunsFile) }

// CredentialsPath is the fallback credential store, used on machines with no OS
// keychain.
func CredentialsPath() string { return filepath.Join(Dir(), Credentials) }

// InstallIDPath is the file holding the anonymous install id.
func InstallIDPath() string { return filepath.Join(Dir(), InstallIDFile) }

// without returns getenv with one key hidden, so a default can be resolved even
// when the caller overrode it.
func without(getenv func(string) string, hidden string) func(string) string {
	return func(key string) string {
		if key == hidden {
			return ""
		}
		return getenv(key)
	}
}
