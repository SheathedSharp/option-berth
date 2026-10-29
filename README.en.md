<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/option-berth-lockup-dark.svg">
    <img src="brand/option-berth-lockup-light.svg" alt="option-berth" width="420">
  </picture>
</p>
<p align="center"><strong>Every checkout, its own berth.</strong><br>Service facts and lifecycle for local Git worktrees.</p>
<p align="center"><a href="README.md">简体中文</a> · English</p>

<p align="center"><a href="https://sheathedsharp.github.io/option-berth/">Website</a> · <a href="https://github.com/SheathedSharp/option-berth/releases/latest">Download the latest engine</a></p>

## Three products

option-berth is three independent products, each with its own repository, version and release cadence:

| Product | Repository | What it does |
| --- | --- | --- |
| **Engine** (this repository) | [SheathedSharp/option-berth](https://github.com/SheathedSharp/option-berth) | `oberth`: declare, observe, start and stop one worktree's services, and answer with facts a person and an agent both read |
| **Client** | [SheathedSharp/option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) | A native macOS surface for services, read-only Git and terminal/agent sessions over the same facts |
| **Coding-agent skill** | [SheathedSharp/option-berth-skill](https://github.com/SheathedSharp/option-berth-skill) | Teaches an agent to read the same facts, run the services, and leave work behind for the next session |

They meet only at the CLI and its `--json` contract: neither the client nor the skill keeps a second
copy of the runtime state. Put the three clones side by side when you develop or try them together
(see "Using the three together" below).

This repository's releases contain only the engine CLI (amd64 and arm64 archives for macOS, Linux and
Windows, plus `SHA256SUMS`). The engine version line starts at `1.0.0`; the pre-split
`v0.2.0`–`v0.8.2` releases belonged to the combined product and are no longer published here. The
client and skill release from their own repositories.

## Why option-berth

Working on several branches, or letting coding agents work across several worktrees, is not only a
code problem: which service belongs to which checkout? Is it actually healthy? Is a worker without a
port still running? Will stopping this project disturb another branch?

option-berth puts **declared services, what is actually running, logs, Git context and the work
record** on one path. You decide what a project should run; `oberth` starts, observes and stops it
within bounds. People and external coding agents read the same facts instead of guessing system state.

## What it changes

| You want to | option-berth gives you |
| --- | --- |
| Run several worktrees at once | Automatic ports and per-run ownership, so branches do not collide |
| Know a service is usable | Process, listener and health kept apart; `up --wait` waits for real readiness |
| Diagnose a failure | Logs, exit result and diagnosis from the service, not a reassembled shell one-liner |
| Observe a portless worker | A run record instead of treating "no port" as "not running" |
| Work with a coding agent | The same CLI as structured JSON; no second model or agent-reasoning system inside |
| Take over someone else's work | `oberth workplace` keeps the commands and tasks run here, and how they ended |

**Not a whole-machine port radar, and not another AI IDE.** `machine:` dependencies are read-only and
project start/stop never takes them over. Worktree isolation is about run ownership and lifecycle
boundaries, not an operating-system sandbox. The interface is not in this repository: it is the
separate [macOS client](https://github.com/SheathedSharp/option-berth-macos).

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

Pin a version with `--version v1.0.0` (`-Version v1.0.0` on Windows), change the directory with
`--dir` / `-Dir`, and skip the PATH edit with `--no-modify-path` / `-NoModifyPath`. The scripts only
download and verify release archives: they start no service and write no Git data. Read
[install.sh](install.sh) / [install.ps1](install.ps1) first if you prefer, or download the
[Releases](https://github.com/SheathedSharp/option-berth/releases) archives directly.

### With go install (Go 1.25+)

```sh
go install github.com/sheathedsharp/option-berth/engine/cmd/oberth@latest

# optional Jev adapter
go install github.com/sheathedsharp/option-berth/engine/cmd/jev-attention@latest
```

This requires v1.1.2 or newer. The binaries land in `$(go env GOPATH)/bin` or `GOBIN`, which must be on
PATH. Uninstall by deleting `oberth` and `jev-attention` from that directory.

Uninstall:

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.sh | sh
```

The uninstaller keeps `~/.option-berth` runtime state unless you pass `--purge-state`. Linux also
ships `.deb` / `.rpm` packages from v1.1.2 onwards. See
[Install and uninstall](docs/install.md) for the full matrix, upgrades and manual archives.

The complete service scenario targets **macOS / Linux**. Windows has a native build, smoke checks and
some fact/persistence tests; that is not full lifecycle support.

### Build from source

The engine needs [Go 1.25 or a later compatible version](engine/go.mod) and Mage. The formal release
rules live in [the release guide](docs/releasing.md).

```sh
git clone https://github.com/SheathedSharp/option-berth.git
cd option-berth
# macOS tooling; use the equivalent Go / Mage install elsewhere
brew install go mage
mage buildEngine
./bin/oberth version --json
```

Building installs nothing and starts no daily service. Once you mean to install, run `mage install`:
it writes the install directory and configures PATH. `NO_MODIFY_PATH=1 mage install` skips the PATH edit.

## Try it first

The repository ships [Hello Worktree](examples/hello-worktree/README.md): one API that only listens on
localhost and one portless worker. Python 3 standard library only — no model account, container or pip
dependency.

```sh
# With oberth installed, from the repository root
cd examples/hello-worktree
oberth up --wait --json
oberth status --json
oberth logs worker --once
oberth down --json
```

For your own project, run `oberth init`, then review and edit the generated `oberth.yaml`. An agent can
draft one, but **a draft is not authorization**: nothing is written until you run `init adopt`.
Fields, automatic ports, dependencies and health checks are in [the CLI guide](docs/cli.md).

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
explicit worktree filter. The optional [Jev adapter](docs/jev-adapter.md) stays outside the engine's
runtime facts and control path, so core features work without any model configuration.

## Working with coding agents

The engine exposes an explicit launch adapter; it implements no model, tool loop or session reasoning:

```sh
oberth agent list --json
oberth agent plan codex --worktree "$PWD" --json
oberth agent run codex --worktree "$PWD"
```

How an agent learns to use these commands lives in the separate
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill) repository. Install it for
the agents already on your machine with the Skills CLI:

```sh
npx skills add SheathedSharp/option-berth-skill -g
```

Claude Code can also install it from the plugin marketplace:
`/plugin install option-berth --marketplace SheathedSharp/option-berth-skill`. Installing it is the
agent's own choice: nothing in this repository installs hooks, skills or MCP configuration for you.

## Using the three together

The three repositories share no code; they meet at the CLI contract. Put them side by side:

```sh
git clone https://github.com/SheathedSharp/option-berth.git
git clone https://github.com/SheathedSharp/option-berth-macos.git
git clone https://github.com/SheathedSharp/option-berth-skill.git
```

The client points at any `oberth` through `BERTH_BIN`; develop the skill by symlinking the sibling
clone, and install the published one with `npx skills add`.

## Versioning and contributing

Versions are **X1.X2.X3**: X1 for a protocol change, X2 for a feature, X3 for a fix; raising a higher
digit clears the lower ones. Release scripts and validation rules are in
[the release guide](docs/releasing.md).

Contribute one focused PR per theme, describing the problem, the implementation, TODOs, tests and
compatibility. Do not commit private configuration, credentials, run logs or screenshots of real
projects. Development rules are in [AGENTS.md](AGENTS.md).

```sh
mage test
mage vet
python3 -m unittest discover -s scripts -p '*_test.py'
```

The core is designed from this product's own needs around worktree, manifest, service, run and runtime
facts. [Product boundaries](docs/product.md) and [architecture](docs/architecture.md) hold the current
contract; the historical origin is recorded in [UPSTREAM](engine/UPSTREAM.md).

## License

Maintained by **SheathedSharp** and contributors. Project code is under the [MIT License](LICENSE);
third-party code and assets keep their own licenses and copyright, see
[third-party notices](THIRD_PARTY_NOTICES.md). Coding-agent names describe compatibility only and do not
imply endorsement by their developers.
