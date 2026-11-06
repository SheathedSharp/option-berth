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

// interpreterRule reads the thing an interpreter actually runs out of its
// arguments, and reports whether it recognized the shape.
type interpreterRule func(invocation) (string, bool)

// interpreterRules are tried in order, most specific first. Each one answers
// only for its own shape, so adding a runtime means adding a rule rather than
// another branch to one long function.
var interpreterRules = []interpreterRule{
	moduleTarget, // python -m foo.bar
	runnerTarget, // uv run uvicorn …, npx next dev
	scriptTarget, // python /path/to/script.py
}

// interpreterAwareBasename extracts a meaningful name from a full command line:
// an app bundle names itself, an interpreter names what it runs, and anything
// else is its own basename.
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
	if !isInterpreter(inv.exec) {
		return inv.exec
	}
	if inv.inlineCode() {
		return inv.exec
	}
	// java is terminal: its own flags decide the target, and a main class is
	// not a script file, so the generic rules must not run after it.
	if inv.exec == "java" {
		if name, ok := javaTarget(inv); ok {
			return name
		}
		return inv.exec
	}
	for _, rule := range interpreterRules {
		if name, ok := rule(inv); ok {
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

// extractJavaMainClass handles java command lines.
// javaTarget reads what a `java` invocation runs: the jar after -jar, the
// module after -m/--module (taking `module/Class` to its class), or the main
// class itself.
func javaTarget(inv invocation) (string, bool) {
	parts := inv.args
	for i := 0; i < len(parts); i++ {
		arg := parts[i]
		if arg == "-jar" {
			if i+1 < len(parts) {
				return filepath.Base(parts[i+1]), true
			}
			return "", false
		}
		// -m/--module <module>[/<mainclass>]: the target is the value itself.
		if arg == "-m" || arg == "--module" {
			if i+1 < len(parts) {
				return javaModuleTarget(parts[i+1]), true
			}
			return "", false
		}
		if v, ok := strings.CutPrefix(arg, "--module="); ok {
			return javaModuleTarget(v), true
		}
		// Flags that take a separate value we must skip so it isn't
		// mistaken for the main class.
		if arg == "-cp" || arg == "-classpath" || arg == "--class-path" ||
			arg == "-p" || arg == "--module-path" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		// First non-flag, non-value token is the main class.
		return arg, true
	}
	return "", false
}

// javaModuleTarget extracts the main class from a `-m` value of the form
// `module/ClassName`, or returns the module name when no class is given.
func javaModuleTarget(v string) string {
	if _, class, ok := strings.Cut(v, "/"); ok {
		return class
	}
	return v
}

// augmentWithCwd prefixes the name with the cwd's basename when that adds
// useful project context. If the cwd basename is itself a noise directory
// (tmp, build, target/release, ...) it walks up the path to find the first
// meaningful ancestor.
func augmentWithCwd(name, cwd string) string {
	if name == "" || cwd == "" {
		return name
	}
	cwdBase := filepath.Base(cwd)
	if cwdBase == "" || cwdBase == "." || cwdBase == "/" {
		return name
	}
	// Walk past noise dirs to find a real project root.
	if isNoiseDir(cwdBase) {
		cwdBase = meaningfulAncestor(cwd)
		if cwdBase == "" {
			return name
		}
	}
	// Don't prepend if the cwd basename is already part of the name.
	if cwdBase == name || strings.HasPrefix(name, cwdBase+"/") {
		return name
	}
	// Don't prepend filesystem-root-ish names that don't identify a project.
	if cwdBase == "Users" || cwdBase == "home" {
		return name
	}
	return cwdBase + "/" + name
}

// meaningfulAncestor walks up from a path, skipping noise directory names,
// to find the first non-noise ancestor. Returns "" if none found.
func meaningfulAncestor(path string) string {
	for i := 0; i < 6; i++ {
		path = filepath.Dir(path)
		base := filepath.Base(path)
		if base == "" || base == "." || base == "/" {
			return ""
		}
		if !isNoiseDir(base) {
			return base
		}
	}
	return ""
}

// isInterpreter reports whether a basename is a scripting language runtime
// whose first arg is typically the meaningful target (a script or module).
func isInterpreter(base string) bool {
	switch base {
	case "python", "python2", "python3",
		"node", "nodejs", "deno", "bun",
		"ruby", "perl", "php",
		"java",
		"uv", "poetry", "pipenv", "pdm", "rye", "npx",
		// A shell wrapping a script names the script, not itself:
		// `/bin/sh /opt/.../rabbitmq-server` is the service.
		"sh", "bash", "zsh", "fish":
		return true
	}
	return false
}

// isOpaqueName reports whether a name is just an interpreter or runtime,
// giving the user no signal about what is actually running.
func isOpaqueName(name string) bool {
	switch name {
	case "", "python", "python2", "python3",
		"node", "nodejs", "deno", "bun",
		"ruby", "perl", "php", "java",
		"sh", "bash", "zsh", "fish",
		"uv", "poetry", "npx",
		// The Erlang VM says nothing; its parent (rabbitmq-server, a release
		// script) does.
		"beam.smp":
		return true
	}
	return false
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
