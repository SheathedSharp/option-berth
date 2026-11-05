package spawn

import (
	"slices"
	"strings"
)

// managerVerbs maps a launcher to the sub-commands that say nothing about what
// is being started. `npm run dev` runs the dev server, not "npm" and not
// "run", so both are stepped over and the name is `dev`.
//
// A launcher listed here also accepts nested launchers (`docker compose up db`)
// because the sub-command's own verb set is merged in as the walk proceeds.
var managerVerbs = map[string][]string{
	"npm":            {"run", "run-script", "exec"},
	"pnpm":           {"run", "exec", "dlx"},
	"yarn":           {"run", "exec", "dlx"},
	"bun":            {"run", "x"},
	"npx":            {},
	"bunx":           {},
	"deno":           {"run", "task"},
	"uv":             {"run", "tool"},
	"uvx":            {},
	"poetry":         {"run"},
	"pipenv":         {"run"},
	"pdm":            {"run"},
	"rye":            {"run"},
	"hatch":          {"run"},
	"python":         {},
	"python3":        {},
	"py":             {},
	"node":           {},
	"go":             {"run"},
	"cargo":          {"run"},
	"docker":         {"compose", "up", "run", "start", "exec"},
	"docker-compose": {"up", "run", "start"},
	"make":           {},
	"just":           {},
	"task":           {},
	"bundle":         {"exec"},
	"dotnet":         {"run"},
	"mvn":            {},
	"gradle":         {},
	"pixi":           {"run"},
	"mise":           {"run", "exec"},
	"nix":            {"run", "develop"},
	"env":            {},
}

// valueFlags swallow the next token, so the token after them is never the
// service name (`npm --prefix ./web run dev`).
var valueFlags = []string{
	"--prefix", "--cwd", "--dir", "-C", "-c",
	"--workspace", "-w", "--filter", "-f",
	"--file", "--project-name", "-p", "--project",
	"--chdir", "--env-file",
}

func takesValue(flag string) bool { return slices.Contains(valueFlags, flag) }

// InferName picks a service name from a command line: the fallback for
// `oberth start` without `--name`.
//
//	npm run serve        -> serve
//	uv run gateway       -> gateway
//	python -m uvicorn    -> uvicorn
//	./run.sh             -> run.sh
//	docker compose up cache -> cache
//
// It never returns a path: the last path element wins, extension included, so
// `./run.sh` stays recognisable and `go run ./cmd/api` becomes `api`.
func InferName(argv []string) string {
	arguments := stripAssignments(argv)
	if len(arguments) == 0 {
		return ""
	}

	binary := commandToken(arguments[0])
	verbs, manager := managerVerbs[binary]
	if !manager {
		return nameFromToken(arguments[0])
	}

	// Copy: nested launchers merge their verbs in and must not mutate the table.
	skip := make(map[string]bool, len(verbs)+4)
	for _, verb := range verbs {
		skip[verb] = true
	}

	for i := 1; i < len(arguments); i++ {
		token := arguments[i]
		if token == "--" {
			continue
		}
		if strings.HasPrefix(token, "-") {
			if takesValue(flagName(token)) && !strings.Contains(token, "=") {
				i++ // its value is not a name either
			}
			continue
		}
		word := commandToken(token)
		if skip[word] {
			continue
		}
		if nested, isLauncher := managerVerbs[word]; isLauncher {
			for _, verb := range nested {
				skip[verb] = true
			}
			continue
		}
		return nameFromToken(token)
	}
	return binary
}

// stripAssignments drops leading `VAR=value` tokens, so `PORT=3000 npm run dev`
// still infers `dev`.
func stripAssignments(argv []string) []string {
	for i, token := range argv {
		key, isAssignment := assignmentKey(token)
		if !isAssignment {
			return argv[i:]
		}
		if key == "" || strings.ContainsAny(key, `/\\.`) {
			return argv[i:]
		}
	}
	return nil
}

func assignmentKey(token string) (string, bool) {
	if strings.HasPrefix(token, "-") || !strings.Contains(token, "=") {
		return "", false
	}
	key, _, _ := strings.Cut(token, "=")
	return key, true
}

// commandToken reduces a token to the binary name it names: no directory, no
// Windows extension, lower-cased for table lookup.
func commandToken(token string) string {
	base := nameFromToken(token)
	for _, suffix := range []string{".exe", ".cmd", ".bat"} {
		base = strings.TrimSuffix(base, suffix)
	}
	return strings.ToLower(base)
}

// nameFromToken is the last path element of a token. Both separators count, so
// a Windows path works on a Unix daemon and the reverse.
func nameFromToken(token string) string {
	token = strings.TrimSuffix(token, "/")
	token = strings.TrimSuffix(token, `\`)
	if at := strings.LastIndexAny(token, `/\`); at >= 0 {
		token = token[at+1:]
	}
	return token
}

// flagName is the flag part of `--flag=value`.
func flagName(token string) string {
	name, _, _ := strings.Cut(token, "=")
	return name
}
