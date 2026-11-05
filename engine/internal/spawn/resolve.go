package spawn

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// Resolution is where a run belongs: the group and name it is registered
// under, plus the `oberth.yaml` that decided them when one did.
type Resolution struct {
	Group      string
	Name       string
	ConfigPath string
}

// Resolve decides the group and name for a command about to be started.
//
//	group: --group | nearest oberth.yaml | git root | base name of cwd
//	name:  --name  | the oberth.yaml service whose cmd is this command
//	       | InferName(argv)
//
// An empty cwd means this process's working directory. A flag always wins
// outright: it is the caller saying they know better than the tree.
func Resolve(cwd string, argv []string, groupFlag, nameFlag string) Resolution {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// One normalisation for the whole chain. The index, the git-root walk and
	// the directory-name fallback all have to see the same spelling, or a cwd
	// reached through a symlink (macOS $TMPDIR, /var against /private/var)
	// resolves to a different group than the directory reached directly.
	cwd = groups.Canonical(cwd)

	index := groups.NewIndex()
	index.Observe(cwd)

	resolution := Resolution{
		Group: strings.TrimSpace(groupFlag),
		Name:  strings.TrimSpace(nameFlag),
	}
	// NearestFor rather than Nearest: a worktree kept inside the main checkout
	// must not pick up the main checkout's file on its way up.
	config := index.NearestFor(cwd)
	if config != nil {
		resolution.ConfigPath = config.Path
	}
	if resolution.Group == "" {
		resolution.Group = groupFor(index, cwd, config)
	}
	if resolution.Name == "" {
		resolution.Name = nameFor(index, cwd, argv, config)
	}
	return resolution
}

// groupFor names the group from the config, else from the checkout, else from
// the directory the command was started in.
//
// The first two are named the way the resolver names them, so a run started in
// a linked worktree lands in `<project>@<worktree>` even when that worktree
// carries a copy of the main checkout's `oberth.yaml`.
func groupFor(index *groups.Index, cwd string, config *groups.Config) string {
	if config != nil && config.Name != "" {
		return index.GroupOf(config)
	}
	if checkout, ok := groups.Locate(cwd); ok {
		return index.CheckoutName(checkout)
	}
	return filepath.Base(cwd)
}

// nameFor finds the service being started, falling back to the command line.
func nameFor(index *groups.Index, cwd string, argv []string, config *groups.Config) string {
	if service, ok := declaredService(index, config, cwd, argv); ok {
		return service
	}
	return InferName(argv)
}

// declaredService finds the service whose `cmd` is the command being started.
// The comparison is on argv, so quoting and repeated spaces in the YAML do not
// matter; a service with its own `cwd` only matches when the command is being
// started there.
func declaredService(index *groups.Index, config *groups.Config, cwd string, argv []string) (string, bool) {
	if config == nil || len(argv) == 0 {
		return "", false
	}
	for _, service := range config.Services {
		if service.Cmd == "" || !sameCommand(SplitCmd(service.Cmd), argv) {
			continue
		}
		if service.Cwd != "" && groups.Canonical(config.ServiceDir(service)) != cwd {
			continue
		}
		return service.Name, true
	}
	return "", false
}

// sameCommand compares a service's declared cmd with a command being started.
// A token carrying a reference (`--port ${port}`) matches whatever it would
// have expanded to, so a command typed out by hand still finds its service.
func sameCommand(declared, started []string) bool {
	if len(declared) != len(started) {
		return false
	}
	for i := range declared {
		if !groups.MatchesExpanded(declared[i], started[i]) {
			return false
		}
	}
	return true
}

// SplitCmd turns an `oberth.yaml` `cmd:` into argv with shell-style quoting and
// no shell execution.
func SplitCmd(s string) []string { return splitCmd(s) }

func splitCmd(s string) []string { return splitCmdFor(s, runtime.GOOS == "windows") }

// splitCmdFor is SplitCmd with the platform injected, so both conventions can
// be tested on one host.
//
// Backslashes follow the host's convention because a `cmd:` is written for the
// machine it runs on. On Unix a backslash escapes the character after it
// outside single quotes, so `/tmp/a\ b` is one argument. On Windows a backslash
// is a path separator — `C:\srv\app.exe` has to survive verbatim — so it is
// literal except before a quote or another backslash inside double quotes,
// which is how CommandLineToArgvW reads a command line.
func splitCmdFor(s string, windows bool) []string {
	var (
		arguments []string
		current   strings.Builder
		started   bool
		quoted    rune // the open quote, or 0 outside one
	)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		char := runes[i]
		switch {
		case quoted == 0 && isSpace(char):
			if started {
				arguments = append(arguments, current.String())
				current.Reset()
				started = false
			}
		case quoted == 0 && (char == '\'' || char == '"'):
			quoted, started = char, true
		case quoted != 0 && char == quoted:
			quoted = 0
		case char == '\\' && i+1 < len(runes) && escapesNext(quoted, runes[i+1], windows):
			i++
			current.WriteRune(runes[i])
			started = true
		default:
			current.WriteRune(char)
			started = true
		}
	}
	if started {
		arguments = append(arguments, current.String())
	}
	return arguments
}

// isSpace reports the characters that separate arguments.
func isSpace(char rune) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r'
}

// escapesNext reports whether a backslash before next is an escape rather than
// a character of its own, inside the given quote (0 for unquoted).
func escapesNext(quoted, next rune, windows bool) bool {
	if !windows {
		// POSIX: a backslash escapes whatever follows, except inside single
		// quotes, which are literal on every platform.
		return quoted != '\''
	}
	// Windows treats a backslash as a path separator, so it only escapes a
	// quote — or the backslash guarding one — inside double quotes.
	return quoted == '"' && (next == '"' || next == '\\')
}
