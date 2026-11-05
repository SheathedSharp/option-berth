package spawn

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// Resolution is the group and name a run is registered under, plus the
// `oberth.yaml` that decided them when there is one.
type Resolution struct {
	Group      string
	Name       string
	ConfigPath string
}

// Resolve implements the first two steps of `oberth start`:
//
//	group: --group | nearest oberth.yaml | git root | base name of cwd
//	name:  --name  | the oberth.yaml service whose cmd is this command
//	       | InferName(argv)
//
// An empty cwd uses the process working directory.
func Resolve(cwd string, argv []string, groupFlag, nameFlag string) Resolution {
	directory := cwd
	if directory == "" {
		directory, _ = os.Getwd()
	}
	// Normalise once for the whole chain: the index, the git-root walk and the
	// directory fallback must see one spelling of this directory, or a cwd
	// reached through a symlink (/var vs /private/var on macOS $TMPDIR) resolves
	// to a different group than the same directory reached directly.
	directory = groups.Canonical(directory)

	index := groups.NewIndex()
	index.Observe(directory)
	// NearestFor, not Nearest: a worktree kept inside the main checkout must
	// not pick up the main checkout's file on the way up.
	config := index.NearestFor(directory)

	resolution := Resolution{
		Group: strings.TrimSpace(groupFlag),
		Name:  strings.TrimSpace(nameFlag),
	}
	if config != nil {
		resolution.ConfigPath = config.Path
	}

	if resolution.Group == "" {
		resolution.Group = inferGroup(index, directory, config)
	}
	if resolution.Name == "" {
		if service, matched := matchService(config, directory, argv); matched {
			resolution.Name = service
		} else {
			resolution.Name = InferName(argv)
		}
	}
	return resolution
}

// inferGroup is the group half of the chain: config, then checkout, then the
// directory the command was started in. The first two use the resolver's own
// spelling, so a run in a linked worktree lands in `<project>@<worktree>` even
// when that worktree carries a copy of the main checkout's manifest.
func inferGroup(index *groups.Index, cwd string, config *groups.Config) string {
	if config != nil && config.Name != "" {
		return index.GroupOf(config)
	}
	if checkout, found := groups.Locate(cwd); found {
		return index.CheckoutName(checkout)
	}
	return filepath.Base(cwd)
}

// matchService finds the service whose `cmd` is the command being started. The
// comparison is on argv, so YAML quoting and repeated spaces do not matter; a
// service with a `cwd` matches only when the command starts there.
func matchService(config *groups.Config, cwd string, argv []string) (string, bool) {
	if config == nil || len(argv) == 0 {
		return "", false
	}
	for _, service := range config.Services {
		if service.Cmd == "" {
			continue
		}
		if !argvMatches(splitCmd(service.Cmd), argv) {
			continue
		}
		if service.Cwd != "" && groups.Canonical(config.ServiceDir(service)) != cwd {
			continue
		}
		return service.Name, true
	}
	return "", false
}

// argvMatches compares a split service command with a command being started. A
// token carrying a reference (`--port ${port}`) matches whatever it would have
// expanded to, so a hand-typed command still finds its service.
func argvMatches(expected, actual []string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for i := range expected {
		if !groups.MatchesExpanded(expected[i], actual[i]) {
			return false
		}
	}
	return true
}

// SplitCmd turns a manifest `cmd:` into argv with shell-style quoting and no
// shell execution.
//
// Backslashes follow the host's convention, because a `cmd:` is written for the
// machine it runs on. On Unix a backslash escapes the following character
// (outside single quotes), so `/tmp/a\ b` is one argument. On Windows a
// backslash is a path separator: `C:\srv\app.exe` survives verbatim, and only a
// quote or the backslash guarding one is escapable inside double quotes, as
// CommandLineToArgvW reads it.
func SplitCmd(text string) []string { return splitCmd(text) }

func splitCmd(text string) []string { return splitCmdFor(text, runtime.GOOS == "windows") }

// splitCmdFor is SplitCmd with the host injected, so both conventions can be
// tested on one machine.
func splitCmdFor(text string, windowsHost bool) []string {
	var (
		arguments []string
		current   strings.Builder
		started   bool
		quote     rune
	)
	symbols := []rune(text)
	for i := 0; i < len(symbols); i++ {
		ch := symbols[i]
		switch {
		case quote == 0 && (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'):
			if started {
				arguments = append(arguments, current.String())
				current.Reset()
				started = false
			}
		case quote == 0 && (ch == '\'' || ch == '"'):
			quote, started = ch, true
		case quote != 0 && ch == quote:
			quote = 0
		case ch == '\\' && i+1 < len(symbols) && escapesNext(quote, symbols[i+1], windowsHost):
			i++
			current.WriteRune(symbols[i])
			started = true
		default:
			current.WriteRune(ch)
			started = true
		}
	}
	if started {
		arguments = append(arguments, current.String())
	}
	return arguments
}

// escapesNext reports whether a backslash before next escapes it rather than
// standing alone, inside the given quote (0 means unquoted).
func escapesNext(quoteChar, following rune, windowsHost bool) bool {
	if quoteChar == '\'' {
		// Single quotes are literal everywhere.
		return false
	}
	if !windowsHost {
		return true
	}
	// Windows: only a quote, or the backslash guarding one, is escapable, and
	// only inside double quotes; elsewhere a backslash is a path separator.
	return quoteChar == '"' && (following == '"' || following == '\\')
}
