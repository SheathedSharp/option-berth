package ports

import (
	"path/filepath"
	"strings"
)

// resolveProcessName names a process from its command line, with its parent's
// command line and working directory as supporting evidence. It is pure: every
// read happens upstream in Enrich.
//
// Two rules run in order. The name comes from the command line, or from the
// parent when the child is only a worker of it — a live-reload supervisor
// leaves the meaningful invocation above. The project context is added after,
// because it describes the process rather than identifying it.
func resolveProcessName(cmd, parentCmd, cwd string) string {
	if cmd == "" {
		return ""
	}
	// An .app names itself completely, and the directory a desktop app was
	// launched from is usually the user's home or a support directory: adding
	// it produces "dev/Warp", not context.
	if name, ok := appBundleName(cmd); ok {
		return name
	}
	return withProjectContext(namedProcess(cmd, parentCmd), cwd)
}

// namedProcess is what the command line runs, preferring the parent's
// invocation when the child only names an interpreter.
func namedProcess(cmd, parentCmd string) string {
	name := interpreterAwareBasename(cmd)
	if !isOpaqueName(name) || parentCmd == "" {
		return name
	}
	if above := interpreterAwareBasename(parentCmd); !isOpaqueName(above) {
		return above
	}
	return name
}

// withProjectContext prefixes the project's directory name when that says more
// than the process name alone.
func withProjectContext(name, cwd string) string {
	if cwd == "" {
		return name
	}
	if augmented := augmentWithCwd(name, cwd); augmented != "" {
		return augmented
	}
	return name
}

// appBundleName returns the outermost app bundle a command belongs to, and
// whether the command runs from one. Every executable inside a bundle names
// the product — including a helper nested in Contents/Frameworks, which exists
// only because the app does. The exception is a bundled interpreter:
// Elasticsearch ships a `jdk.app`, and `java` running from it is still java,
// not "jdk".
func appBundleName(cmd string) (string, bool) {
	if base := firstTokenBasename(cmd); base != "" && isInterpreter(base) {
		return "", false
	}
	idx := strings.Index(cmd, ".app/")
	if idx < 0 {
		return "", false
	}
	return filepath.Base(cmd[:idx]), true
}

// firstTokenBasename is the basename of a command line's executable token. It
// splits on the first space, which is wrong for the path itself when the path
// contains spaces — it is only used to classify the executable, and a
// mis-split path still lands on a plausible name for that.
func firstTokenBasename(cmd string) string {
	token, _, _ := strings.Cut(strings.TrimSpace(cmd), " ")
	if token == "" {
		return ""
	}
	return filepath.Base(token)
}

// invocation is one command line split into what ran and what it was given.
type invocation struct {
	exec string   // basename of argv[0]
	args []string // argv[1:]
}

// parseInvocation splits a command line, reporting false when it is empty.
func parseInvocation(cmd string) (invocation, bool) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return invocation{}, false
	}
	return invocation{exec: filepath.Base(fields[0]), args: fields[1:]}, true
}

// inlineCode reports a `python -c …` / `node -e …` invocation, where the next
// token is source rather than a filename.
func (inv invocation) inlineCode() bool {
	return len(inv.args) > 0 && (inv.args[0] == "-c" || inv.args[0] == "-e")
}

// interpreterRule reads the thing a runtime actually runs out of its
// arguments, and reports whether it recognized the shape.
type interpreterRule func(invocation) (string, bool)

// runtimeRule is what one executable does with its arguments: the shapes to
// try, in order, and whether the bare name tells a reader anything. The two
// facts are not the same question — a package runner names the tool it hands
// off to, pipenv names itself — so one row carries both instead of two lists
// that can drift apart.
type runtimeRule struct {
	targets []interpreterRule
	opaque  bool
}

var (
	// runsScript names a module or script given as an argument.
	runsScript = runtimeRule{targets: []interpreterRule{moduleTarget, scriptTarget}, opaque: true}
	// runsTool hands its arguments to the tool that follows the runner verb.
	runsTool = runtimeRule{targets: []interpreterRule{moduleTarget, runnerTarget}, opaque: true}
	// runsItself preserves the prior rule set, where pipenv/pdm/rye counted as
	// interpreters but their bare names were still shown. Whether that should
	// match runsTool is a behavior question, not a naming one; keep it until
	// there is evidence for either answer.
	runsItself = runtimeRule{targets: []interpreterRule{moduleTarget, runnerTarget}}
)

// runtimeRules is the one table of executables that hide their target in the
// argument list. An executable absent here is its own name.
var runtimeRules = map[string]runtimeRule{
	"python": runsScript, "python2": runsScript, "python3": runsScript,
	"node": runsScript, "nodejs": runsScript, "deno": runsScript, "bun": runsScript,
	"ruby": runsScript, "perl": runsScript, "php": runsScript,
	"sh": runsScript, "bash": runsScript, "zsh": runsScript, "fish": runsScript,
	"uv": runsTool, "poetry": runsTool, "npx": runsTool,
	"pipenv": runsItself, "pdm": runsItself, "rye": runsItself,
	// java selects its target with its own flags; a main class is not a script
	// file, so it is terminal and the generic shapes never follow it.
	"java": {targets: []interpreterRule{javaTarget}, opaque: true},
	// The Erlang VM says nothing; its parent, a release script, does.
	"beam.smp": {opaque: true},
}

// interpreterAwareBasename extracts a meaningful name from a full command line:
// an app bundle names itself, a runtime names what it runs, and anything else
// is its own basename.
func interpreterAwareBasename(cmd string) string {
	// An .app path may contain spaces ("Application Support"), so it is matched
	// before the command line is split.
	if name, ok := appBundleName(cmd); ok {
		return name
	}
	inv, ok := parseInvocation(cmd)
	if !ok {
		return ""
	}
	runtime, hidesTarget := runtimeRules[inv.exec]
	if !hidesTarget || inv.inlineCode() {
		return inv.exec
	}
	for _, target := range runtime.targets {
		if name, ok := target(inv); ok {
			return name
		}
	}
	return inv.exec
}

// moduleTarget reads `python -m foo.bar` as "foo.bar". A non-flag argument
// before -m means the interpreter was given a file, so this rule declines.
func moduleTarget(inv invocation) (string, bool) {
	for i, arg := range inv.args {
		if arg == "-m" && i+1 < len(inv.args) {
			return inv.args[i+1], true
		}
		if !strings.HasPrefix(arg, "-") {
			return "", false
		}
	}
	return "", false
}

// packageRunners are the front-ends whose first non-flag argument names the
// tool they hand off to. `npx next dev` runs next; `uv run uvicorn …` runs
// uvicorn.
var packageRunners = map[string]bool{
	"uv": true, "poetry": true, "npx": true,
	"pipenv": true, "pdm": true, "rye": true,
}

// runnerTarget reads `npx next dev` and `uv run uvicorn …` as the tool after
// the runner verb. A runner not in packageRunners declines.
func runnerTarget(inv invocation) (string, bool) {
	if !packageRunners[inv.exec] {
		return "", false
	}
	rest := inv.args
	// These front-ends take a "run" verb before the command; npx takes the
	// command directly.
	if len(rest) > 1 && rest[0] == "run" {
		rest = rest[1:]
	}
	for _, arg := range rest {
		if !strings.HasPrefix(arg, "-") {
			return filepath.Base(arg), true
		}
	}
	return "", false
}

// scriptTarget reads `python /path/script.py` as "script.py": the first
// non-flag argument.
func scriptTarget(inv invocation) (string, bool) {
	for _, arg := range inv.args {
		if !strings.HasPrefix(arg, "-") {
			return filepath.Base(arg), true
		}
	}
	return "", false
}

// javaValueFlags are the java options whose next token is a value: stepping
// over that token is what keeps a classpath from being read as the main class.
var javaValueFlags = map[string]bool{
	"-cp": true, "-classpath": true, "--class-path": true,
	"-p": true, "--module-path": true,
}

// javaTarget reads what a `java` invocation runs: the jar after -jar, the
// module after -m/--module, or the first token that is neither a flag nor a
// flag's value. Argument order decides; the first target seen wins.
func javaTarget(inv invocation) (string, bool) {
	args := inv.args
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-jar":
			if i+1 < len(args) {
				return filepath.Base(args[i+1]), true
			}
			return "", false
		case arg == "-m" || arg == "--module":
			if i+1 < len(args) {
				return moduleAndClass(args[i+1]), true
			}
			return "", false
		case strings.HasPrefix(arg, "--module="):
			return moduleAndClass(strings.TrimPrefix(arg, "--module=")), true
		case javaValueFlags[arg]:
			i++ // the value belongs to this flag, not to the class
		case !strings.HasPrefix(arg, "-"):
			return arg, true
		}
	}
	return "", false
}

// moduleAndClass splits a `module/Class` target to its class; a bare module
// name is already the answer.
func moduleAndClass(value string) string {
	if _, class, hasClass := strings.Cut(value, "/"); hasClass {
		return class
	}
	return value
}

// augmentWithCwd prefixes the name with the project the process runs in when
// that says more than the process name alone.
func augmentWithCwd(name, cwd string) string {
	if name == "" || cwd == "" {
		return name
	}
	project := projectContextName(cwd)
	switch project {
	case "", "Users", "home": // filesystem roots identify no project
		return name
	}
	if project == name || strings.HasPrefix(name, project+"/") {
		return name // already present; do not repeat it
	}
	return project + "/" + name
}

// projectContextName is the cwd component worth showing. Generic build and
// temp directories say nothing about which project runs there, so the walk
// goes up past them; a path with no other name has no context to add.
func projectContextName(cwd string) string {
	// Six ancestors plus the cwd itself, the same reach the walk it replaces
	// had before it gave up.
	const ancestors = 6
	for depth := 0; depth <= ancestors; depth++ {
		base := filepath.Base(cwd)
		if base == "" || base == "." || base == "/" || base == "\\" {
			return ""
		}
		if !isNoiseDir(base) {
			return base
		}
		cwd = filepath.Dir(cwd)
	}
	return ""
}

// isInterpreter reports whether a basename is a runtime whose arguments name
// the real target (a script, a module or the tool it hands off to).
func isInterpreter(base string) bool {
	return len(runtimeRules[base].targets) > 0
}

// isOpaqueName reports whether a bare name tells a reader nothing about what
// is actually running.
func isOpaqueName(name string) bool {
	if name == "" {
		return true
	}
	return runtimeRules[name].opaque
}

// noiseDirs are generic build and temp directory names: seeing one in a
// working directory says nothing about which project is running there, so cwd
// augmentation walks past it.
var noiseDirs = map[string]bool{
	"tmp": true, "temp": true, "build": true, "dist": true,
	"bin": true, "out": true, "target": true, "release": true,
	"debug": true, "cmd": true, "src": true, "pkg": true,
	"node_modules": true, ".bin": true, ".venv": true, "venv": true,
	"env": true, "__pycache__": true, ".cache": true, ".tox": true,
}

// isNoiseDir reports whether a directory name identifies no particular project.
func isNoiseDir(name string) bool { return noiseDirs[name] }
