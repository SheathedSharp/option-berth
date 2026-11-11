<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/option-berth-lockup-dark.svg">
    <img src="brand/option-berth-lockup-light.svg" alt="option-berth" width="420">
  </picture>
</p>
<p align="center"><strong>Stop guessing what's running.</strong><br>The runtime facts layer for Git worktrees: what a checkout should run, whether it really is running, and why not.</p>
<p align="center">English · <a href="README.zh.md">简体中文</a></p>

<p align="center"><a href="https://oberth.zayju.de/">Website</a> · <a href="https://oberth.zayju.de/guide/">Guide</a> · <a href="docs/product.md">Product</a> · <a href="docs/cli.md">CLI</a> · <a href="https://github.com/SheathedSharp/option-berth/releases/latest">Latest release</a> · <a href="https://oberth.zayju.de/llms.txt">llms.txt</a></p>

## Why this exists

Worktrees solved parallel code, not parallel runtime. With four checkouts open — and an agent writing in
each — every question about a running service turns into a guess: which checkout does it belong to, is
it actually healthy, is the worker without a port still alive, and will stopping this project disturb
another branch? The answers sit in a process table, a port list, three log files and someone's memory.

`oberth` puts them on one chain: **worktree → manifest → service → run → runtime facts**. You confirm
what a checkout should run; the engine starts, observes and stops it within bounds; people and coding
agents read the same facts instead of reconstructing system state.

## What you get

| You want to | option-berth gives you |
| --- | --- |
| Run several worktrees at once | Per-run ownership and automatic ports, so branches do not collide |
| Know a service is usable | Process, listener and health kept apart; `up --wait` waits for real readiness |
| Diagnose a failure | The service's log, exit result and diagnosis, not a reassembled shell one-liner |
| Observe a portless worker | A run record, instead of treating "no port" as "not running" |
| Let a script or agent read the facts | The same CLI as structured JSON; no second model or agent-reasoning system inside |
| Hand work to the next session | `oberth workplace` keeps the commands and tasks run here, and how they ended |

Stopping never crosses into another checkout. When a checkout disappears, `worktrees --gone` lists the
identities it left behind and `down --worktree` still stops them and returns their `port: auto`
reservations; deleting data stays a person's call.

**Not a machine-wide port radar, not an OS sandbox, not another AI IDE.** `machine:` dependencies are
read-only and project start/stop never takes them over; worktree isolation is about run ownership and
lifecycle boundaries.

## Try it

The repository ships [Hello Worktree](examples/hello-worktree/README.md): one API that only listens on
localhost and one portless worker, Python 3 standard library only — no model account, container or pip
dependency. [Worktree Data](examples/worktree-data/README.md) adds a database per worktree through
`${worktree}`, and [Stack Workshop](examples/stack-workshop/README.md) is a ten-service stack with a
slow-starting auth, a built frontend, a migration and read-only `machine:` dependencies.

```sh
# With oberth installed, from the repository root
cd examples/hello-worktree
oberth up --wait --json
oberth status --json
oberth logs worker --once
oberth down --json
```

For your own project, run `oberth init`, then review and edit the generated `oberth.yaml`. An agent can
draft one, but **a draft is not authorization**: nothing is written until you run `init adopt`. Fields,
automatic ports, dependencies and health checks are in [the CLI guide](docs/cli.md).

## Install

macOS / Linux: one command resolves the latest release, downloads the archive for this machine,
verifies SHA256, and installs into `~/.local/bin`.

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex
```

Homebrew, Linux `.deb`/`.rpm`, pinned versions, uninstall, upgrades and the platform
boundaries are in [Install and uninstall](docs/install.md). The scripts only download and verify release
archives: they start no service and write no Git data.

Running `oberth` shows the installed version and, when a newer release exists, the upgrade command for
this platform. `oberth version --check` asks on demand; it only reads release metadata and never
downloads or installs anything.

The complete service lifecycle is supported on **macOS and Linux**. Windows has a native build, smoke
checks and some fact/persistence tests, which is not full lifecycle support.

## Everyday commands

```sh
oberth status --json            # this worktree's declaration and runtime facts
oberth doctor                  # install, manifest and background-service diagnosis
oberth up --wait                # start and wait for readiness
oberth restart --only api       # restart only the named service
oberth logs api --once          # read a service's log
oberth down                    # stop the project and release port claims safely
```

`oberth git` gives read-only Git context; for cross-project observation use `oberth events` with an
explicit worktree filter.

## With coding agents and orchestrators

The engine exposes an explicit launch adapter and implements no model, tool loop or session reasoning.
How an agent learns to use these commands lives in the separate
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill) repository:

```sh
npx skills add SheathedSharp/option-berth-skill -g
oberth agent plan codex --worktree "$PWD" --json
```

Conductor, Cursor, Claude Code, Codex and similar tools create the workspace; call `oberth` from their
own setup / run / cleanup scripts or an outer wrapper:

```sh
oberth up --wait                           # setup for a new workspace
oberth status --json                       # scripts and agents read the runtime facts
oberth down --worktree /path/to/worktree   # cleanup; still works after the checkout is gone
```

The product installs no hooks, registers no MCP server and edits none of those tools' configuration.
Per-tool recipes, version prerequisites and the port flags common frameworks need (Vite wants
`--port ${port} --strictPort`) are in [the integration recipes](docs/integrations.md).

The native macOS client is a separate product,
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos). It reads this CLI's facts and
keeps no second copy of the runtime state. Both products, and the skill, meet only at the CLI and its
`--json` contract.

## Documentation

| What you need | Where to go |
| --- | --- |
| Product definition, what it refuses to do | [docs/product.md](docs/product.md) |
| CLI, JSON fields, manifest, error codes | [docs/cli.md](docs/cli.md) |
| Install, upgrade, uninstall | [docs/install.md](docs/install.md) |
| Orchestrators and dev servers | [docs/integrations.md](docs/integrations.md) |
| Work records and handoff | [docs/workplace.md](docs/workplace.md) |
| Implementation boundaries | [docs/agent/architecture.md](docs/agent/architecture.md) |
| What each check proves | [docs/agent/validation.md](docs/agent/validation.md) |
| Build, test and release this repository | [docs/agent/develop.md](docs/agent/develop.md), [docs/agent/releasing.md](docs/agent/releasing.md) |

## Contributing

Contribute one focused PR per theme, describing the problem, the implementation, TODOs, tests and
compatibility. Do not commit private configuration, credentials, run logs or screenshots of real
projects. The rules this repository expects before you touch it are in [AGENTS.md](AGENTS.md).

```sh
mage test
mage vet
python3 -m unittest discover -s scripts -p '*_test.py'
```

## License

Maintained by **SheathedSharp** and contributors. Project code is under the [MIT License](LICENSE);
third-party code and assets keep their own licenses and copyright, see
[third-party notices](THIRD_PARTY_NOTICES.md). Coding-agent names describe compatibility only and do not
imply endorsement by their developers.
