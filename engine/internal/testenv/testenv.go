// Package testenv gives a Go test binary a private machine to run on.
//
// Every path option-berth resolves from the environment — the config directory,
// the database, the socket, the daemon log, the run logs — hangs off HOME, so a
// test that reaches any of them reaches the developer's live install. Isolation
// is therefore a property of the whole binary, not of individual tests: every
// package whose tests can reach the daemon or the config directory calls Run
// from its TestMain, and from then on nothing in that binary can find the real
// install even if it tries.
//
// A run also gets a private root under the machine's temp directory, which
// becomes its TMPDIR (Root). t.TempDir, os.CreateTemp and a harness that builds
// the real binary with `go build -o` all write inside it, so the leak gate can
// name the daemons this run is responsible for without touching another
// `go test` running beside it on the same machine.
//
// A package adopts it with one line in TestMain — `os.Exit(testenv.Run(m))` —
// or keeps control by handling its own helper-process mode first and calling Run
// for the rest.
//
// It imports nothing from the module on purpose: this package is linked into
// every test binary that uses it, and a helper that pulled in the daemon and
// SQLite would make a display-only test binary far heavier than its subject. A
// package that wants to prove its own resolvers agree calls Isolated.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The environment option-berth reads to locate anything. Setting all of them is
// what makes a run private; anything added to the product's own list has to be
// added here too.
const (
	noAutostartEnv = "BERTH_NO_AUTOSTART"
	socketEnv      = "BERTH_SOCKET"
	homeEnv        = "BERTH_HOME"
	logDirEnv      = "BERTH_LOG_DIR"
	dbEnv          = "BERTH_DB"
	allowTestEnv   = "BERTH_ALLOW_TEST_DAEMON"
	gateAllEnv     = "BERTH_TESTENV_GATE_ALL"
)

// Layout is the private world one test run gets. It is filled in once, by
// Isolate, before any test runs.
type Layout struct {
	// Root is this run's temp root: the directory TMPDIR points at while the
	// tests run, and the one a harness building a binary should build into so
	// the leak gate recognises what it starts.
	Root string
	// Home is the isolated HOME, inside Root.
	Home string
	// socketDir holds the daemon socket. It is a sibling of Home rather than a
	// child because macOS caps a unix socket path at about 104 bytes.
	socketDir string
	// realHome and realTemp are what this run displaced. Isolate is the only
	// chance to read them, because afterwards the process answers with Root.
	realHome string
	realTemp string
}

// current is the layout in force. It is set once, before any test runs.
var current Layout

// Run is what a TestMain calls: it isolates the process, runs the tests, and
// returns the code os.Exit should get.
func Run(m *testing.M) int {
	session := begin()
	failures := m.Run()
	leaked := AssertNoLeakedDaemons()
	session.end()
	return exitCode(failures, leaked)
}

// exitCode keeps a failing run failing, and turns "every test passed but the
// suite left a daemon behind" into a failure of its own. Both reasons are a
// failure; neither may mask the other.
func exitCode(failures int, leaked []Daemon) int {
	if failures != 0 || len(leaked) == 0 {
		return failures
	}
	return 1
}

// session is one isolated run: the layout it built and the cleanup it owns.
type session struct{ layout Layout }

func begin() session {
	return session{layout: Isolate()}
}

func (s session) end() {
	if s.layout.Root != "" {
		_ = os.RemoveAll(s.layout.Root)
	}
}

// Isolate points every path option-berth resolves from the environment at a
// private temp directory, switches daemon autostart off, and returns the layout
// it built.
//
// It exits the process rather than returning an error when it cannot make the
// environment safe: a test binary that keeps going here writes to the
// developer's real install, and there is no test result worth that.
func Isolate() Layout {
	current = newLayout()
	adopt(current)
	requirePrivate(current)
	return current
}

// newLayout builds this run's directories and records what they displaced.
func newLayout() Layout {
	l := Layout{realHome: mustUserHomeDir(), realTemp: os.TempDir()}
	preserveGoEnv(l)

	// The names are terse because they prefix every socket path, and macOS
	// caps a unix socket at about 104 bytes.
	root, err := os.MkdirTemp("", "snrun")
	if err != nil {
		die("creating this run's temp root: %v", err)
	}
	l.Root = root
	l.Home = filepath.Join(root, "h")
	l.socketDir = filepath.Join(root, "s")

	for _, dir := range []string{
		l.socketDir,
		filepath.Join(l.Home, ".option-berth"),
		filepath.Join(l.Home, "run"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			die("creating %s: %v", dir, err)
		}
	}
	return l
}

// adopt makes l the world this process and every child it spawns live in.
func adopt(l Layout) {
	setTempDir(l.Root)
	set("HOME", l.Home)        // unix
	set("USERPROFILE", l.Home) // windows

	// Inherited directory overrides outrank HOME. Remove them rather than
	// pinning them to one path: each test can still install its own HOME or
	// explicit overrides without sharing another test's state and logs.
	unset(homeEnv, logDirEnv)
	// A leftover opt-in from the developer's shell must not disarm the guard
	// that keeps a test binary from becoming a daemon.
	unset(allowTestEnv)

	set("XDG_CONFIG_HOME", filepath.Join(l.Home, ".config")) // freedesktop
	set("XDG_RUNTIME_DIR", filepath.Join(l.Home, "run"))     // outranks HOME for the socket
	set(dbEnv, filepath.Join(l.Home, ".option-berth", "option-berth.db"))
	set(socketEnv, socketPath(l.socketDir))
	set(noAutostartEnv, "1")
}

// requirePrivate proves the environment now resolves somewhere else. A sandbox
// that silently ignored Setenv would otherwise leave every test pointing at the
// live install.
func requirePrivate(l Layout) {
	if l.realHome != "" && sameDir(os.Getenv("HOME"), l.realHome) {
		die("HOME is still %s; refusing to run tests against the live install", l.realHome)
	}
	if !Isolated(filepath.Join(l.Home, ".option-berth")) {
		die("the isolated config directory is not under %s", l.Root)
	}
	if !sameDir(os.TempDir(), l.Root) {
		die("os.TempDir() is %s, not this run's root %s", os.TempDir(), l.Root)
	}
}

// setTempDir makes dir the temp directory of this process and of every child it
// spawns, so that everything a test writes outside its own HOME still lands in a
// directory this run owns. Unix reads TMPDIR; Windows reads TEMP then TMP, so
// all three are set.
func setTempDir(dir string) {
	vars := []string{"TMPDIR"}
	if os.PathSeparator == '\\' {
		vars = []string{"TMP", "TEMP"}
	}
	for _, key := range vars {
		set(key, dir)
	}
}

// preserveGoEnv pins the Go toolchain's caches where they already are.
//
// Go derives GOPATH, GOCACHE, GOMODCACHE and GOENV from the home directory, and
// several integration harnesses run `go build` to produce the real `oberth`
// binary they drive. Moving HOME without this points those builds at an empty
// module cache inside a temp directory that is deleted at the end of the run:
// they re-download the whole module graph, and offline they simply fail.
// Resolving the defaults here, while HOME is still the real one, is exactly what
// the toolchain would have done. A variable already set in the environment is
// left alone — it does not depend on HOME in the first place.
func preserveGoEnv(l Layout) {
	// Each toolchain variable and where the toolchain would have derived it from.
	derived := map[string]string{}
	if cache, err := os.UserCacheDir(); err == nil {
		derived["GOCACHE"] = filepath.Join(cache, "go-build")
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		derived["GOENV"] = filepath.Join(cfg, "go", "env")
	}
	if l.realHome != "" {
		derived["GOPATH"] = filepath.Join(l.realHome, "go")
	}
	for key, value := range derived {
		setIfUnset(key, value)
	}
	// GOMODCACHE is derived from GOPATH, so it is resolved after the loop above
	// has settled where GOPATH points.
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		setIfUnset("GOMODCACHE", filepath.Join(gopath, "pkg", "mod"))
	}
}

// setIfUnset installs a derived default, leaving an environment that already
// names the variable alone: such a value does not depend on HOME.
func setIfUnset(key, value string) {
	if os.Getenv(key) != "" || value == "" {
		return
	}
	set(key, value)
}

// Root is this run's private temp root: the directory TMPDIR points at while
// the tests run, and the one a harness building a binary should build into so
// the leak gate recognises what it starts. It is empty before Isolate runs.
func Root() string { return current.Root }

// Home is the isolated HOME, inside Root.
func Home() string { return current.Home }

// RealHome is the home directory this run displaced: the one the developer's
// live oberth install lives in.
func RealHome() string { return current.realHome }

// ConfigDir is the isolated ~/.option-berth.
func ConfigDir() string { return filepath.Join(current.Home, ".option-berth") }

// Isolated reports whether every path lies inside this run's own temp root. It
// is what a package's own TestMain uses to check the real resolvers agree:
//
//	if !testenv.Isolated(config.Path(), store.Path(), daemon.SocketPath()) { ... }
func Isolated(paths ...string) bool {
	if len(paths) == 0 || current.Root == "" {
		return false
	}
	for _, p := range paths {
		if p == "" || !under(p, current.Root) {
			return false
		}
	}
	return true
}

// RequireIsolated fails the test unless every path is inside this run's own temp
// root.
func RequireIsolated(t testing.TB, paths ...string) {
	t.Helper()
	for _, p := range paths {
		RequireIsolatedPath(t, p)
	}
}

// RequireIsolatedPath is RequireIsolated for one path, naming what leaked.
func RequireIsolatedPath(t testing.TB, path string) {
	t.Helper()
	if Isolated(path) {
		return
	}
	t.Fatalf("%s is outside the isolated test environment (HOME=%s, TMPDIR=%s)",
		path, current.Home, current.Root)
}

// AllowAutostart lets one test use the real autostart path, and restores the ban
// afterwards. Only a test that owns the daemon it starts and stops it in a
// cleanup may call it.
func AllowAutostart(t testing.TB) {
	t.Helper()
	t.Setenv(noAutostartEnv, "")
}

// ChildEnv is what a harness appends to a child process's environment when that
// child is the real `oberth` binary and is allowed to autostart a daemon of its
// own. The child is not a test binary, so only the process-wide ban has to be
// lifted.
func ChildEnv() string { return noAutostartEnv + "=" }

// socketPath is where this run's daemon socket lives. Named pipes share one
// flat namespace on Windows, so a test binary there takes an address stamped
// with its pid instead of a path.
func socketPath(dir string) string {
	if os.PathSeparator == '\\' {
		return fmt.Sprintf(`\\.\pipe\option-berth-test-%d`, os.Getpid())
	}
	return filepath.Join(dir, "d.sock")
}

func mustUserHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func set(key, value string) {
	if err := os.Setenv(key, value); err != nil {
		die("setting %s: %v", key, err)
	}
}

func unset(keys ...string) {
	for _, key := range keys {
		if err := os.Unsetenv(key); err != nil {
			die("unsetting %s: %v", key, err)
		}
	}
}

// under reports whether path lies inside dir, by comparing the resolved
// spellings so a symlinked /var and /private/var are the same place.
func under(path, dir string) bool {
	if dir == "" {
		return false
	}
	inside, outside := resolve(path), resolve(dir)
	if inside == outside {
		return true
	}
	return strings.HasPrefix(inside, outside+string(filepath.Separator))
}

func sameDir(a, b string) bool { return resolve(a) == resolve(b) }

// resolve cleans a path and follows symlinks where it can. /var on macOS is a
// symlink to /private/var, so os.TempDir() and a path built from HOME can name
// the same directory in two spellings. A path that does not exist yet — a
// database file a test has not created — still has to resolve, so the walk stops
// at the deepest ancestor that does and keeps the rest verbatim.
func resolve(path string) string {
	clean := filepath.Clean(path)
	// Walk up to the deepest ancestor that exists, remembering the tail.
	tail := ""
	for candidate := clean; ; {
		real, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			return filepath.Join(real, tail)
		}
		up := filepath.Dir(candidate)
		if up == candidate {
			// Nothing on this path resolves — not even the root.
			return clean
		}
		tail = filepath.Join(filepath.Base(candidate), tail)
		candidate = up
	}
}

// die reports why isolation could not be made safe and stops the binary. There
// is no test result worth continuing for: everything after this point would
// write to the developer's real install.
func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "testenv: %s\n", fmt.Sprintf(format, args...))
	os.Exit(1)
}
