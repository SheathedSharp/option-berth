package ports

import (
	"path/filepath"
	"slices"
	"strings"
)

// resolveProcessName turns a command line, its parent's command line and the
// working directory into a name a person can read. It is pure: all I/O happens
// upstream in Enrich.
//
// The chain is:
//  1. a macOS .app bundle names its product;
//  2. the interpreter-aware basename of the command;
//  3. the parent process, when the command itself is only an opaque runtime;
//  4. the working directory, prepended when it adds project context.
func resolveProcessName(cmd, parentCmd, cwd string) string {
	if cmd == "" {
		return ""
	}
	if product, isBundle := bundleProduct(cmd); isBundle {
		return product
	}

	resolved := describeCommand(cmd)
	if opaqueName(resolved) {
		resolved = parentDescription(resolved, parentCmd)
	}
	return withDirectory(resolved, cwd)
}

// parentDescription replaces an opaque name with the parent's description,
// when the parent has a better one. Live-reload supervisors and
// multiprocessing workers keep the meaningful invocation in the parent.
func parentDescription(name, parentCmd string) string {
	if parentCmd == "" {
		return name
	}
	parentName := describeCommand(parentCmd)
	if opaqueName(parentName) {
		return name
	}
	return parentName
}

// withDirectory prepends cwd context when there is any.
func withDirectory(name, cwd string) string {
	if cwd == "" {
		return name
	}
	if augmented := augmentWithCwd(name, cwd); augmented != "" {
		return augmented
	}
	return name
}

// bundleProduct returns the outermost .app bundle's name. Every executable
// inside a bundle belongs to the product, including a helper nested under
// Contents/Frameworks. A bundled interpreter is the exception: Elasticsearch
// ships a jdk.app, and java running from it is still java, not "jdk".
func bundleProduct(cmd string) (string, bool) {
	if stem := executableBase(cmd); stem != "" && interpreterName(stem) {
		return "", false
	}
	at := strings.Index(cmd, ".app/")
	if at < 0 {
		return "", false
	}
	return filepath.Base(cmd[:at]), true
}

// executableBase is the basename of the command's first token. Splitting on
// the first space is wrong when the path itself contains spaces; the result is
// only used to classify the executable, so a mis-split still yields a
// plausible name.
func executableBase(cmd string) string {
	token, _, _ := strings.Cut(strings.TrimSpace(cmd), " ")
	if token == "" {
		return ""
	}
	return filepath.Base(token)
}

// describeCommand extracts a meaningful name from a full command line: bundle,
// interpreter, inline code, module, runner subcommand or script.
func describeCommand(cmd string) string {
	// Match bundles before splitting on whitespace: their paths may contain
	// spaces.
	if product, isBundle := bundleProduct(cmd); isBundle {
		return product
	}

	tokens := strings.Fields(cmd)
	if len(tokens) == 0 {
		return ""
	}
	binary := filepath.Base(tokens[0])

	// Inline-code flags: `python -c "…"` and `node -e "…"` are followed by
	// source, not a filename. Return the interpreter and let the caller fall
	// back to the parent process.
	if len(tokens) >= 2 && (tokens[1] == "-c" || tokens[1] == "-e") {
		return binary
	}

	if !interpreterName(binary) {
		return binary
	}

	switch {
	case moduleTarget(tokens) != "":
		return moduleTarget(tokens)
	case runnerTarget(tokens) != "":
		return runnerTarget(tokens)
	case binary == "java":
		if target := javaTarget(tokens); target != "" {
			return target
		}
		return binary
	case scriptTarget(tokens) != "":
		return scriptTarget(tokens)
	}
	return binary
}

// moduleTarget handles `python -m foo.bar` -> "foo.bar".
func moduleTarget(tokens []string) string {
	for i := 1; i < len(tokens)-1; i++ {
		if tokens[i] == "-m" {
			return tokens[i+1]
		}
		if !strings.HasPrefix(tokens[i], "-") {
			return ""
		}
	}
	return ""
}

// runnerTarget handles `uv run uvicorn …`, `poetry run gunicorn …` and
// `npx next dev`: skip the runner verb and use the subcommand's basename.
func runnerTarget(tokens []string) string {
	if len(tokens) < 2 {
		return ""
	}
	runner := filepath.Base(tokens[0])
	switch runner {
	case "uv", "poetry", "pipenv", "pdm", "rye":
		// These take a verb such as "run" before the subcommand.
		start := 1
		if len(tokens) >= 3 && tokens[1] == "run" {
			start = 2
		}
		return firstPlainToken(tokens[start:])
	case "npx":
		return firstPlainToken(tokens[1:])
	}
	return ""
}

// firstPlainToken is the first token that is not a flag, as a basename.
func firstPlainToken(tokens []string) string {
	for _, token := range tokens {
		if !strings.HasPrefix(token, "-") {
			return filepath.Base(token)
		}
	}
	return ""
}

// scriptTarget handles `python /path/script.py` -> "script.py".
func scriptTarget(tokens []string) string {
	return firstPlainToken(tokens[1:])
}

// javaTarget handles java invocations: -jar, -m/--module, or the main class
// after any flags and their values.
func javaTarget(tokens []string) string {
	for i := 1; i < len(tokens); i++ {
		token := tokens[i]
		if token == "-jar" {
			if i+1 < len(tokens) {
				return filepath.Base(tokens[i+1])
			}
			return ""
		}
		if token == "-m" || token == "--module" {
			if i+1 < len(tokens) {
				return moduleClass(tokens[i+1])
			}
			return ""
		}
		if value, hasPrefix := strings.CutPrefix(token, "--module="); hasPrefix {
			return moduleClass(value)
		}
		// Flags that take a separate value must skip it, so the value is not
		// mistaken for the main class.
		switch token {
		case "-cp", "-classpath", "--class-path", "-p", "--module-path":
			i++
			continue
		}
		if strings.HasPrefix(token, "-") {
			continue
		}
		return token
	}
	return ""
}

// moduleClass reads a `-m` value of the form `module/ClassName`, or returns
// the module name when no class is given.
func moduleClass(value string) string {
	if _, class, hasClass := strings.Cut(value, "/"); hasClass {
		return class
	}
	return value
}

// augmentWithCwd prefixes the name with the cwd's basename when that adds
// project context. A noise directory is skipped by walking up to the first
// meaningful ancestor.
func augmentWithCwd(name, cwd string) string {
	if name == "" || cwd == "" {
		return name
	}
	base := filepath.Base(cwd)
	if base == "" || base == "." || base == "/" {
		return name
	}
	if noiseDir(base) {
		base = meaningfulAncestor(cwd)
		if base == "" {
			return name
		}
	}
	// The directory already appears in the name.
	if base == name || strings.HasPrefix(name, base+"/") {
		return name
	}
	// Filesystem roots do not identify a project.
	if base == "Users" || base == "home" {
		return name
	}
	return base + "/" + name
}

// meaningfulAncestor walks up from a path, skipping noise directories, and
// returns the first real one. "" when there is none within six levels.
func meaningfulAncestor(path string) string {
	current := path
	for depth := 0; depth < 6; depth++ {
		current = filepath.Dir(current)
		name := filepath.Base(current)
		if name == "" || name == "." || name == "/" {
			return ""
		}
		if !noiseDir(name) {
			return name
		}
	}
	return ""
}

// interpreterNames are the runtimes whose first argument is usually the
// meaningful target; a shell wrapping a script names the script, not itself.
var interpreterNames = []string{
	"python", "python2", "python3",
	"node", "nodejs", "deno", "bun",
	"ruby", "perl", "php",
	"java",
	"uv", "poetry", "pipenv", "pdm", "rye", "npx",
	"sh", "bash", "zsh", "fish",
}

// opaqueNames are the names that say nothing about what is running. The
// Erlang VM says nothing; its parent (rabbitmq-server) does.
var opaqueNames = []string{
	"", "python", "python2", "python3",
	"node", "nodejs", "deno", "bun",
	"ruby", "perl", "php", "java",
	"sh", "bash", "zsh", "fish",
	"uv", "poetry", "npx",
	"beam.smp",
}

// noiseDirectories are generic build and temp directories that do not
// identify a project.
var noiseDirectories = []string{
	"tmp", "temp", "build", "dist", "bin", "out", "target",
	"release", "debug", "cmd", "src", "pkg",
	"node_modules", ".bin", ".venv", "venv", "env",
	"__pycache__", ".cache", ".tox",
}

func interpreterName(binary string) bool { return slices.Contains(interpreterNames, binary) }

func opaqueName(name string) bool { return slices.Contains(opaqueNames, name) }

func noiseDir(name string) bool { return slices.Contains(noiseDirectories, name) }
