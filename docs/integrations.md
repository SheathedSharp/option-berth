# Integrating orchestration tools and dev servers

English | [中文](integrations.zh.md)

option-berth is the **runtime fact layer for worktrees**. Orchestration tools create workspaces,
schedule sessions, and often allocate their own ports; `oberth` answers what state the services on
this code are in right now, why a failure failed, and stops the services it left behind when things
end.

Integration works by **the user calling the CLI in their own scripts**: where a tool offers setup /
run / cleanup slots, paste the `oberth` commands into them; where it has no slots, use an outer
wrapper or a note in `AGENTS.md`. The user creates these scripts and configuration themselves;
option-berth does not install hooks, does not register an MCP, does not modify any tool's
configuration, and does not distribute integration code; the interface is still only the CLI and
`--json`.

## Version prerequisite

The cleanup steps on this page use `down --worktree`, `down --identity`, and `worktrees`, available
from v1.3.0. Earlier versions enter the checkout directory and run `oberth down` **before** deleting
it.

## The three commands

### After creation: start and wait for readiness

```sh
cd /path/to/checkout
oberth up --wait
```

- `--wait` waits until the services selected for this run are actually listening; services that
  declare `health:` must also pass their health check; a portless worker counts as ready based on the
  run record being active. Each service declares its own readiness budget with `ready_timeout:` (Go
  duration, default 30s, must be positive); `up --wait` waits by it by default, and `depends_on`
  successors also wait by the depended-on service's own budget. `--wait-timeout` is only the overall
  cap for the whole `up --wait` run; it only tightens, never loosens, each service's
  `ready_timeout:`; slow services should write their budget into the manifest rather than just
  raising this flag.
- `ready_timeout:` constrains the listen and health waits **after startup**, including dependency
  waits and `up --wait`; `--wait-timeout` only constrains `up --wait`'s own wait (it starts counting
  after the `groups.start` stream ends, and does not affect the daemon's dependency waits). Neither
  constrains `prepare:`. Slow dependency installation needs the `prepare_inputs:` cache (skipped when
  the content is unchanged), or a larger script timeout on the orchestration tool's own side.
- On failure the exit code is 1, with `state`, `reason`, and `hint` given per service; `hint` points
  at `oberth logs <service> --once` only if the process really did start.
- When observation is blind, `up --wait` returns an `observation_*` error immediately and keeps the
  run it just started. This is not "it didn't start". Follow the `hint`, run `oberth serve --detach`
  in a normal terminal, then retry; do not retry repeatedly inside a sandbox.
- `up` by default refuses service working directories outside `$HOME`. When an orchestration tool
  puts the workspace outside home, confirm and add `--allow-outside-home`; `down` has no such flag.
- The manifest must be confirmed by a human: `oberth init`, or `oberth init draft` to draft and a
  human `oberth init adopt` to adopt. An agent's draft is not authorization.

### While running: read the facts

```sh
oberth status --json
oberth logs <service> --once --json
```

The parts of `status --json` most relevant to orchestration are:

- `worktree.services[]`: `pid`, `run_id`, `port_actual`, `running`, `health_status`,
  `last_exit.reason`. `exits[].hint` is synonymous with `services[].hint` in `up --json`: the
  engine's deterministic next step based on whether the log file exists; `worktree.services[]` itself
  has no `hint`.
- `worktree.identity` (`wt_…`) and `scope.root`: for stopping and for checking the checkout root
  path.
- Top-level `observation`: appears when the listener table is untrustworthy, carrying `state`,
  `code`, `detail`, `hint`. None of the `observation_*` codes mean "nothing is listening", and still
  less "not running".

Do not substitute the instantaneous output of `ss` / `lsof` / `/proc` for these fields; they have no
run ownership and cannot tell "cannot see it" apart from "not running".

### Ending: stop by worktree

```sh
oberth down                                         # directory still exists, use the most recent manifest
oberth down --worktree /absolute/path/to/checkout   # by checkout root path
oberth down --identity wt_...                       # by identity, when the directory is already deleted
```

- `--worktree` wants the **checkout root path**, not a subdirectory; take it from `scope.root` in
  `status --json`, or from `git rev-parse --show-toplevel`.
- When the directory is already deleted, the daemon can still stop the runs and listeners it left
  behind by identity ledger and hand back the `port: auto` reservation; the precondition is that the
  daemon has seen this checkout before.
- `down` hands back the reservation only after it has confirmed the stop. On cancellation, an
  incomplete stop, an observation failure after the action, runs/listeners still registered, or a
  reservation that has already been refreshed, it keeps the reservation and errors; do not take the
  error as a release.
- When the target no longer has any runs or reservations, an explicit identity is still a successful
  empty stop.
- `oberth worktrees --gone` lists only the identities the daemon has recorded; it is not a complete
  inventory of orphan processes. A ledger row with no manifest has no service group to stop, and
  `down --identity` returns `not_found`.
- When two checkouts occupy the same group name, a group-scoped stop returns `ambiguous`; in that
  case name the target by port or pid for `kill`; do not guess one on the caller's behalf.

## General integration

| Tool slot | What to put there |
|---|---|
| Create / setup | `oberth up --wait` (add `--json` when a machine needs to read the output) |
| Run / run | Do not start the same set of services again. Services started by `oberth` belong to the daemon; the tool stopping the run script will not stop them |
| Archive / delete / cleanup | `oberth down --worktree <checkout root>`; where that is not available, enter the directory and run `oberth down` before deleting |
| Only a single run slot | Put the start into setup; do not disguise setup as the run slot, or the tool will hold onto it or kill it |

A few easy pitfalls:

- **PATH**: setup / hooks often do not load your shell rc. Use the absolute path to `oberth`, or set
  `PATH` explicitly in the script; the examples on this page append `~/.local/bin` per the default
  install location in the README — if you installed elsewhere, substitute the real path. The same
  environment is passed to the services' `cmd:`; making only `oberth` executable is not enough —
  `node`, `npm`, and `python3` must be on PATH too. After starting, check again with
  `oberth logs <service> --once`.
- **The workspace root is not the Git checkout root**: `up` finds the nearest `oberth.yaml` from the
  cwd. If the tool's workspace directory contains several levels of directories, first
  `cd "$(git rev-parse --show-toplevel)"`, and pass the same root to `down --worktree`.
- **Do not hardcode `PORT`**: for a `port: auto` service the engine allocates the port and injects it
  through the environment as `PORT`. A service's own `env:` takes precedence over the injected
  `PORT`, so `env: PORT: 3000` overrides the allocated value and makes `up --wait` wait on the wrong
  port. Reference `${port}`, or use `env: { PORT: "${port}" }`.
- **Strict port**: if the framework quietly switches to another port when the port is taken,
  `up --wait` waits for the port in the manifest and eventually fails with `ready_timeout` (and
  cleans up only the runs started by this invocation). Prefer the framework's strict-port option, so
  the conflict becomes a readable failure fact directly.

## Tool recipes

All the configuration below is created by the user; project-level configuration
(`.conductor/settings.toml`, `.cursor/worktrees.json`, a project's `.claude/settings.json`,
`.codex/`) can be committed with the repo, while user-level configuration (such as
`~/.codex/hooks.json`, user-level Claude Code settings) stays on the local machine. option-berth does
not write these files.

These scripts are only enabled in a repo where a human has already confirmed `oberth.yaml`; without a
manifest `up` fails, so run `oberth init` first and confirm the start command and port.

### Conductor

Conductor configures `[scripts]` in `.conductor/settings.toml` in the repo; personal overrides go in
`.conductor/settings.local.toml`, and user-level defaults in `~/.conductor/settings.toml`. setup runs
after the workspace is created and Git is checked out; archive runs before the workspace is archived.

```toml
# <repo>/.conductor/settings.toml (written by the user, committed with the repo)
"$schema" = "https://conductor.build/schemas/settings.repo.schema.json"

[scripts]
setup = 'PATH="$HOME/.local/bin:$PATH" oberth up --wait'
archive = 'PATH="$HOME/.local/bin:$PATH" oberth down --worktree "$CONDUCTOR_WORKSPACE_PATH"'
```

- Do not put `oberth up --wait` into `scripts.run.<id>`. That is the action behind the Run button,
  and Conductor sends SIGTERM to its process group on Stop; services started by oberth belong to the
  daemon and are not held by this action.
- setup / archive run from the workspace directory; Conductor creates a Git worktree for the
  workspace, and `CONDUCTOR_WORKSPACE_PATH` is usually that checkout root. First confirm it matches
  `scope.root` from `oberth status --json`, then pass the same value to `down --worktree`; if they do
  not match, use `scope.root`.
- `CONDUCTOR_PORT` is a block of 10 ports that Conductor allocates itself. Do not let the same
  service get its port allocated by two mechanisms at once: services managed by oberth use
  `port: auto`; if Conductor's preview requires binding `CONDUCTOR_PORT`, leave that service outside
  the oberth manifest, or reference it only read-only as `machine:`.
- archive is the only cleanup entry point in the docs; there is no separate delete hook. Archiving in
  Conductor interrupts a setup that is running; the official docs do not say whether the archive hook
  always runs after that interruption, so check important workspaces yourself.
- Whether a failed setup prevents workspace creation, and how long the script timeout is, the
  official docs do not say.

### Cursor

Cursor reads `.cursor/worktrees.json` in the repo and runs the setup command when it creates a
workspace. The command can be an array, or a relative path to a script under `.cursor/`; the script
form gives better control over quoting and PATH.

```json
{
  "setup-worktree-unix": "oberth-setup.sh"
}
```

Full service scenarios on Windows are outside this repo's validation scope, see the
[validation notes](agent/validation.md); this page does not provide a Windows version of the setup
script.

```sh
#!/bin/sh
# .cursor/oberth-setup.sh
set -eu
cd "$(git rev-parse --show-toplevel)"
exec /absolute/path/to/oberth up --wait
```

- Cursor's docs say the commands run in sequence in the worktree, but do not say which shell is used
  or how PATH is inherited; use absolute paths in the script, and `cd` to the Git root explicitly.
- **There is no teardown hook.** Cursor's automatic cleanup deletes the worktree directory; the docs
  do not say that user commands are run on deletion. Run `oberth down --worktree <checkout root>`
  before deleting; if you forget, after deletion use `oberth worktrees --gone` to find the identity,
  then `oberth down --identity wt_…` (subject to the version prerequisite in the previous section).
- Whether a failed setup prevents the agent from running, and whether it is re-run, the official docs
  do not say; `oberth up --wait` is safe to run repeatedly.
- When debugging, watch the `Worktrees Setup` channel in the Output panel.

### Claude Code

Claude Code has two paths; decide which one first:

**A. Do not take over worktree creation (recommended)**. Keep Claude's own `git worktree` behavior,
and put the `AGENTS.md` / `CLAUDE.md` section at the end of this page into the repo, so that the agent
reads `oberth status --json` first when needed and then uses `oberth up --wait`. If you create the
worktree in an outer script, start and clean up in that script:

```sh
#!/bin/sh
set -eu
worktree=$(cd "$1" && pwd -P)
shift
export PATH="$HOME/.local/bin:$PATH"
trap 'oberth down --worktree "$worktree"' EXIT
cd "$worktree"
oberth up --wait
claude "$@"
```

`trap` guarantees cleanup even when `claude` fails or is interrupted; services started by `oberth` are
held by the daemon and do not disappear automatically when the script exits. For slow services (for
example a Next.js first compile), declare the budget with `ready_timeout:` in the manifest;
`--wait-timeout` can only tighten it, not replace it.

**B. Let Claude Code's WorktreeCreate take over creation**. Use this only when you really need
automatic creation plus startup. Configuring `WorktreeCreate` **replaces** Claude Code's default
`git worktree` creation: `.worktreeinclude`, base and branch selection are no longer handled
automatically, the hook must create the directory itself, and it must print the directory path as the
last non-empty line of stdout, with all other output going to stderr. A non-zero exit makes workspace
creation fail.

Put the following hooks into the project-level `.claude/settings.json` (committable) or user-level
settings; the example is project-level:

```json
{
  "hooks": {
    "WorktreeCreate": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "${CLAUDE_PROJECT_DIR}/.claude/hooks/worktree-create.sh",
            "timeout": 600
          }
        ]
      }
    ],
    "WorktreeRemove": [
      {
        "hooks": [
          { "type": "command", "command": "${CLAUDE_PROJECT_DIR}/.claude/hooks/worktree-remove.sh" }
        ]
      }
    ]
  }
}
```

```sh
#!/bin/sh
# worktree-create.sh; stdin is the hook JSON, the last line of stdout must be the worktree path
set -eu
export PATH="$HOME/.local/bin:$PATH"
payload=$(cat)
name=$(printf '%s' "$payload" | jq -r .name)
root=$(git rev-parse --show-toplevel)
dir="$root/.claude/worktrees/$name"
git worktree add "$dir" >&2
cleanup() {
  oberth down --worktree "$dir" >&2 || true
  branch=$(git -C "$dir" rev-parse --abbrev-ref HEAD 2>/dev/null || true)
  git worktree remove --force "$dir" >&2 || true
  if [ -n "$branch" ] && [ "$branch" != "HEAD" ]; then
    git -C "$root" branch -D "$branch" >&2 || true
  fi
}
trap cleanup EXIT
cd "$dir"
oberth up --wait >&2
trap - EXIT
echo "$dir"
```

```sh
#!/bin/sh
# worktree-remove.sh
set -eu
export PATH="$HOME/.local/bin:$PATH"
payload=$(cat)
p=$(printf '%s' "$payload" | jq -r .worktree_path)
case "$p" in
  /*) ;;
  *) echo "worktree_path must be absolute" >&2; exit 1 ;;
esac
oberth down --worktree "$p" >&2 || exit 1
branch=$(git -C "$p" rev-parse --abbrev-ref HEAD 2>/dev/null || true)
common=$(git -C "$p" rev-parse --git-common-dir 2>/dev/null || true)
git worktree remove --force "$p"
if [ -n "$branch" ] && [ "$branch" != "HEAD" ] && [ -n "$common" ]; then
  git --git-dir="$common" branch -D "$branch" >&2 || true
fi
```

- Put the scripts in `.claude/hooks/` and commit them with the project; `${CLAUDE_PROJECT_DIR}` points
  at the project root from when the session started.
- These two events have no matcher; even if you write one it is ignored. The `jq` in the examples
  needs to be installed separately.
- `WorktreeRemove` only fires for worktrees created by `WorktreeCreate`, and does not fire when a
  subagent / workflow agent ends; worktrees created with the default `git worktree` have no
  documented leaving event.
- hooks run with the user's full privileges, not inside a sandbox; a `.claude/settings.json` committed
  to the repo becomes commands that execute on users' machines. Interactive sessions have a workspace
  trust confirmation; `-p` / SDK sessions do not have this confirmation.
- Do not use `SessionEnd` for cleanup: the default budget is 1.5s, at most 60s, and `/clear` and
  `/resume` also trigger it.
- `WorktreeCreate` only covers worktrees created by Claude Code itself; checkouts created by other
  orchestration tools do not trigger it, and there you need the tool's own setup / cleanup.

### Codex

Codex has **no** worktree-created / worktree-removed hook. The most reliable approach is an outer
wrapper:

```sh
#!/bin/sh
set -eu
worktree=$(cd "$1" && pwd -P)
shift
export PATH="$HOME/.local/bin:$PATH"
trap 'oberth down --worktree "$worktree"' EXIT
cd "$worktree"
oberth up --wait
codex "$@"
```

`trap` likewise guarantees cleanup when Codex fails. Do not let both `oberth up` and `codex` start the
same set of services; start once, and have Codex read the facts through `oberth status --json`.

Codex's `SessionStart` / `SessionEnd` hooks are the closest lifecycle entry points, but they are not
worktree events, and the following behaviors are not stated in the official docs, so they cannot be
treated as guarantees:

- whether `codex exec` / `codex exec --worktree` runs these hooks;
- whether hook commands are restricted by the Codex sandbox (if they are, `oberth up` may not be able
  to write state or bind local ports);
- whether hooks inherit the environment, including `PATH`, `HOME`, `BERTH_HOME`;
- whether `SessionEnd`'s default 1s / maximum 3s is enough for `oberth down` to finish.

If you accept these unknowns, you can start with `SessionStart` (matcher `startup|fork`) and do
best-effort cleanup with `SessionEnd` in the user-level `~/.codex/hooks.json`; use the absolute path
to `oberth` in the scripts, and do not treat `SessionEnd` as the only cleanup path. Project-level
`.codex/` is only loaded when the project is trusted, and a path newly created by `--worktree` may be
outside the trusted scope, so prefer user level.

The Codex in the ChatGPT desktop app has a separate Local environments setup script (desktop app
only, configured under the project's `.codex/`): it runs automatically when a new worktree is created
and can hold `oberth up --wait`; it has no corresponding delete hook, so cleanup still has to run
`oberth down` before deletion.

Worktrees managed by Codex itself (`codex --worktree`, `/worktree`) are deleted according to a
retention policy; the official docs do not say that user commands are run on deletion. Run
`oberth down --worktree <checkout root>` before deletion, or use `worktrees --gone` /
`down --identity` to stop them afterwards.

### Other tools

As long as a tool has setup / run / cleanup slots, paste the three commands per "General
integration". Where there is no cleanup slot, put cleanup in an outer script or as the last step
before deletion; do not rely on deleting the directory itself to stop the services.

## The section to paste into AGENTS.md / CLAUDE.md

Put the following section into `AGENTS.md` at the repo root (Claude Code also reads `CLAUDE.md`, and
you can append it per tool). It solves the most common problem: when a port is taken, the agent first
runs `ss`, directly executes `python3 app.py`, digs through `/proc`, and only much later thinks of
`oberth`.

````md
## Local services

The services in this worktree are managed by option-berth (`oberth`). Before starting or
troubleshooting, read the facts with it first: do not guess ports first, dig through `ps` / `lsof` /
`/proc`, or directly run commands like `npm run dev` or `python3 app.py`.

- Run `oberth status --json` first. Service state is in `worktree.services[]`: `last_exit.reason` is
  the most recent failure reason and `health_status.reason` is the health-check reason; when the
  listener data is untrustworthy, read the top-level `observation` (`state`, `code`, `hint`).
- Start with `oberth up --wait --json`. `--wait` waits until the services are actually listening and
  pass their declared health checks; a portless worker counts as ready based on the run record being
  active.
- `services[].hint` in `oberth up --json` and `exits[].hint` in `status --json` are the same kind of
  deterministic next step; `worktree.services[]` itself has no `hint`.
- On failure, read the service's own output with `oberth logs <service> --once --json`; do not
  substitute a wait timeout or a port scan for the reason.
- When `observation.state` is not `observable`, say explicitly that you "cannot see the listener",
  and follow `observation.hint`; do not treat it as "not running", and do not claim the service is
  healthy.
- Stop the current worktree with `oberth down`, or
  `oberth down --worktree "$(git rev-parse --show-toplevel)"`. If the directory is already deleted,
  use `oberth worktrees --gone` to find the `wt_…`, then `oberth down --identity wt_…` (a ledger row
  with no manifest has no service group to stop). Unless the user explicitly names a port or pid, do
  not kill services by port or by global process name.
- When there is no `oberth.yaml`, run `oberth init` and leave the fields that need human confirmation
  to the user; you can draft with `oberth init draft`, but only run `oberth init adopt` after a human
  confirms.
- Do not start or stop services referenced by `machine:` / `workspace:`; they are read-only and are
  not part of this worktree's lifecycle.

If `oberth down --help` on this machine has no `--worktree`, use `oberth down` before deleting the
worktree directory.
````

## Framework recipes

`port: auto` is the right choice for most local dev services: the engine reserves a different port for
each worktree and injects the port into `PORT`; `${port}` in `cmd:` is expanded per service before
startup. `cmd:` is split into argv shell-style but does not go through a shell, so do not wrap the
whole command in `sh -c` unless you really need shell semantics.

| Framework | `cmd:` | Port behavior |
|---|---|---|
| Vite | `npm run dev -- --port ${port} --strictPort` | Does not read `PORT`; without `--strictPort` it quietly switches ports. `--` passes the arguments through to the dev script |
| SvelteKit | `npm run dev -- --port ${port} --strictPort` | The dev server is Vite, behavior as above; requires the generated project's dev script to be `vite dev` |
| Next.js | `npx next dev -p ${port}` | The explicit `-p` makes a taken port fail outright; there is no `--strictPort`. It only switches ports automatically when the port comes from the default value 3000 |
| Nuxt | `npx nuxt dev --port ${port} --strictPort` | `--strictPort` exits when the port is unavailable instead of switching ports |
| Astro | `npx astro dev --port ${port}` | The docs have no strict-port option; when the port is taken it tries the next port, and `up --wait` times out on the manifest port |
| Django | `python manage.py runserver 127.0.0.1:${port}` | The port is a positional argument; when taken it errors out immediately, with no automatic port switching |
| Flask | `flask run --port ${port}` | Errors out immediately when taken; reads `FLASK_RUN_PORT`, not `PORT` |
| Rails | `bin/rails server -p ${port}` | Errors out immediately when taken; without `-p` it reads `PORT` |
| Go | The app's own flag / environment variable | `net/http` has no standard port argument; `go run . --port ${port}` only holds when the app parses `--port` itself |
| Node / Express | `node server.js` | Works directly when the app reads `process.env.PORT` itself; otherwise pass `${port}` to the app's own argument |

A complete minimal manifest:

```yaml
name: web
services:
  - name: web
    cmd: npm run dev -- --port ${port} --strictPort
    port: auto
    health: /
```

`health: /` suits a dev server that returns 2xx/3xx on the root path; when there is no usable health
path, delete that line and the readiness decision falls back to port listening. A portless worker
omits `port:` and its activity is judged by the run record:

```yaml
services:
  - name: worker
    cmd: python3 -u worker.py
    env:
      API_URL: ${api.url}
```

## Troubleshooting

| Symptom | What to check first |
|---|---|
| `up --wait` reports `observation_*` | The current process cannot see the listener table; it is not that the service failed to start. Following the `hint`, run `oberth serve --detach` in a normal terminal, or run `oberth doctor --only listener_observation` |
| `up --wait` times out on `ready_timeout` | The service is not listening on the manifest port, or the health check did not pass. Check `oberth logs <service> --once`; when the framework quietly switches ports, switch to a strict-port option |
| The failure reason is `port_occupied` | A fixed port is taken by another process. Stop the occupant or change the port; do not widen the target with `pkill` |
| `down --worktree` returns `not_found` | What you passed is not the recorded checkout root, or the ledger row has no manifest to stop. Use `scope.root` from `status --json`; prefer `oberth down` before deletion |
| `down` returns `ambiguous` | Two checkouts share the same group name. Name the target by port or pid for `kill`; do not pick one on the caller's behalf |
| `oberth` or `node` / `npm` not found in setup | The script did not load your shell rc. Use absolute paths or set `PATH` in the script; a service's `cmd:` inherits the same environment |
| A service fails to start outside home | `up`'s default restriction applies to the service working directory. After confirming, add `--allow-outside-home`; `down` has no such flag |

## Validation boundaries

The `oberth` commands, fields, and error semantics on this page follow this repo's
[CLI contract](cli.md); third-party configuration comes from each tool's official docs as of
2026-10-31. These scripts have **not** actually been run end to end in every combination of
Conductor, Cursor, Claude Code, and Codex; after tools upgrade, the official docs take precedence.
Agent sessions inside a sandbox may not see the listener table; that is not a service failure, see the
observation boundaries in the [validation notes](agent/validation.md).
