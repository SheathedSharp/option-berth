package spawn

import "strings"

// launcherTables lists, one launcher per line, the binary followed by the
// sub-commands that only select *what* to run. `npm run dev` starts a dev
// server, so npm and run both carry no information and the walk steps over
// them.
//
// A launcher also accepts a nested launcher (`docker compose up db`): the
// sub-command's own verbs merge in as the walk descends.
const launcherTables = `
npm            run run-script exec
pnpm           run exec dlx
yarn           run exec dlx
bun            run x
npx
bunx
deno           run task
uv             run tool
uvx
poetry         run
pipenv         run
pdm            run
rye            run
hatch          run
python
python3
py
node
go             run
cargo          run
docker         compose up run start exec
docker-compose up run start
make
just
task
bundle         exec
dotnet         run
mvn
gradle
pixi           run
mise           run exec
nix            run develop
env
`

// launcherVerbs is launcherTables parsed into a lookup the walk can consult.
var launcherVerbs = parseLaunchers(launcherTables)

// parseLaunchers turns the table into binary → set of sub-commands it hides.
// An empty verb set is meaningful: the binary is still a launcher (`npx vite`
// names vite), it just has no sub-commands of its own to skip.
func parseLaunchers(table string) map[string]map[string]bool {
	launchers := make(map[string]map[string]bool)
	for _, line := range strings.Split(table, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 {
			continue
		}
		verbs := make(map[string]bool, len(words)-1)
		for _, verb := range words[1:] {
			verbs[verb] = true
		}
		launchers[words[0]] = verbs
	}
	return launchers
}

// valueTakingFlags swallow the token after them, so that token is never the
// name: in `npm --prefix ./web run dev` the name is `dev`, not `./web`.
var valueTakingFlags = map[string]bool{
	"--prefix": true, "--cwd": true, "--dir": true, "-C": true, "-c": true,
	"--workspace": true, "-w": true, "--filter": true, "-f": true,
	"--file": true, "--project-name": true, "-p": true, "--project": true,
	"--chdir": true, "--env-file": true,
}

// InferName derives a service name from a command line — the fallback when
// `oberth start` is given no `--name`.
//
//	npm run dev            => dev
//	uv run api             => api
//	python -m uvicorn      => uvicorn
//	./dev.sh               => dev.sh
//	docker compose up db   => db
//
// The result is never a path: the last element wins, extension included, so a
// `./dev.sh` stays recognisable while `go run ./cmd/api` becomes `api`.
func InferName(argv []string) string {
	tokens := withoutAssignments(argv)
	if len(tokens) == 0 {
		return ""
	}

	head := launcherName(tokens[0])
	verbs, isLauncher := launcherVerbs[head]
	if !isLauncher {
		// An ordinary command names itself; its arguments describe what it
		// works on, not what the run is.
		return lastElement(tokens[0])
	}

	// Copy the verb set: a nested launcher merges its own verbs in, and the
	// table itself must stay as written.
	skipped := make(map[string]bool, len(verbs)+4)
	for verb := range verbs {
		skipped[verb] = true
	}

	for i := 1; i < len(tokens); i++ {
		token := tokens[i]
		if token == "--" {
			continue
		}
		if strings.HasPrefix(token, "-") {
			if valueTakingFlags[flagKey(token)] && !strings.Contains(token, "=") {
				i++ // the flag's value is not a name either
			}
			continue
		}
		word := launcherName(token)
		if skipped[word] {
			continue
		}
		if nested, ok := launcherVerbs[word]; ok {
			for verb := range nested {
				skipped[verb] = true
			}
			continue
		}
		return lastElement(token)
	}
	// Nothing beyond the launcher itself: `npm` alone is named `npm`.
	return head
}

// withoutAssignments drops leading `VAR=value` tokens, so `PORT=3000 npm run
// dev` still infers `dev`. A token containing a path separator is an argument,
// not an assignment, and stops the scan.
func withoutAssignments(argv []string) []string {
	for i, token := range argv {
		if strings.HasPrefix(token, "-") || !strings.Contains(token, "=") {
			return argv[i:]
		}
		key, _, _ := strings.Cut(token, "=")
		if key == "" || strings.ContainsAny(key, "/\\.") {
			return argv[i:]
		}
	}
	return nil
}

// launcherName reduces a token to the binary it names: no directory, no Windows
// extension, lower-cased so the table lookup is case-insensitive. Lower-casing
// comes first because Windows is case-insensitive too: `NPM.EXE` is npm.
func launcherName(token string) string {
	base := strings.ToLower(lastElement(token))
	for _, extension := range []string{".exe", ".cmd", ".bat"} {
		base = strings.TrimSuffix(base, extension)
	}
	return base
}

// lastElement is the final path element of a token, honouring both separators
// so a Windows path resolves on a Unix daemon and the other way round.
func lastElement(token string) string {
	token = strings.TrimSuffix(strings.TrimSuffix(token, "/"), `\`)
	if i := strings.LastIndexAny(token, `/\`); i >= 0 {
		token = token[i+1:]
	}
	return token
}

// flagKey is the flag part of `--flag=value`.
func flagKey(token string) string {
	key, _, _ := strings.Cut(token, "=")
	return key
}
