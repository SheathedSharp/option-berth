<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/option-berth-lockup-dark.svg">
    <img src="brand/option-berth-lockup-light.svg" alt="option-berth — a berth for every worktree" width="420">
  </picture>
</p>
<p align="center"><strong>A berth for every worktree.</strong><br>A local service workspace for Git worktrees.</p>
<p align="center"><a href="README.md">简体中文</a> · English</p>

## Why option-berth

Working on several branches—or running coding agents in parallel worktrees—creates questions that Git alone
cannot answer. Which checkout owns this service? Is it actually healthy? Is a worker without a listening port
still running? Can you stop this project without disrupting another branch?

option-berth brings **service declarations, observed runs, logs, and Git context** into one workflow.
You approve what a project should run. `oberth` starts it, observes it, and stops it within explicit ownership
boundaries. People and external coding agents use the same facts rather than guessing separately.

## What it helps you do

| Your task | What option-berth provides |
| --- | --- |
| Run multiple worktrees | Automatic ports and separate run ownership |
| Confirm readiness | Process, listener, and health distinctions; `up --wait` waits for readiness |
| Investigate failures | Service logs, exit results, and focused diagnostics |
| Manage portless workers | Run records instead of treating “no port” as “not running” |
| Work with coding agents | The same CLI with structured JSON; no built-in model or agent reasoning system |
| Use a native desktop interface | A macOS view of the same service facts and read-only Git context |

**Not a machine-wide port radar or another AI IDE.** `machine:` dependencies are read-only and are not taken
over by project lifecycle commands. Worktree isolation means ownership and lifecycle separation, not an
operating-system security sandbox.

## Build and install

Source builds are available now. The engine requires [Go 1.25 or a compatible later version](engine/go.mod)
and Mage; the macOS client also needs system development tools. See the [release process](docs/releasing.md).

```sh
git clone https://github.com/SheathedSharp/option-berth.git
cd option-berth
# macOS build tools; use the appropriate Go / Mage installation on other systems
brew install go mage
mage buildEngine
./bin/oberth version --json
```

Building does not install software or start your everyday services. To install explicitly, run `mage install`.
It writes installation directories, configures PATH, and installs the desktop application on macOS.
Use `NO_MODIFY_PATH=1 mage install` to leave shell PATH configuration unchanged.

Complete service scenarios target **macOS and Linux**. Windows has native builds, smoke tests, and selected
fact/persistence tests—not complete lifecycle coverage. The desktop client currently targets
**macOS 14+ on Apple Silicon**.

## Try it first

[Hello Worktree](examples/hello-worktree/README.md) contains a loopback-only API and a portless worker.
It uses the Python 3 standard library: no model account, container, or pip dependencies are required.

```sh
# After installing oberth, start at the repository root
cd examples/hello-worktree
oberth up --wait --json
oberth status --json
oberth logs worker --once
oberth down --json
```

For your own project, run `oberth init`, then review and edit `oberth.yaml`. An existing coding agent may
help draft it, but **a draft is not authorization**: only an explicit, reviewed `init adopt` writes it to the
project. See the [CLI reference](docs/cli.md) for ports, dependencies, health checks, and the manifest format.

## Everyday commands

```sh
oberth status --json            # Declarations and observed runtime for this worktree
oberth doctor                  # Installation, manifest, and daemon diagnostics
oberth up --wait                # Start and wait for readiness
oberth restart --only api       # Restart only the named service
oberth logs api --once          # Read service logs
oberth down                    # Stop this project and safely handle port reservations
```

`oberth git` exposes read-only Git context. For cross-project observation, use `oberth events` with explicit
worktree filters. The product does not automatically install other tools' hooks, skills, or MCP configuration.
The optional [Jev adapter](docs/jev-adapter.md) stays outside the engine's runtime facts and control path;
core functionality does not require model configuration.

## Versioning and contributing

Versions use **X1.X2.X3**: protocol changes, features, and bug fixes respectively. Incrementing a higher
component resets the lower components. The [release process](docs/releasing.md) defines scripts and gates.

Submit focused PRs with the problem, implementation, TODOs, tests, compatibility, and rollback boundaries.
Do not commit private configuration, credentials, runtime logs, or real project screenshots.
See [AGENTS.md](AGENTS.md) for development conventions.

```sh
mage test
mage vet
python3 -m unittest discover -s scripts -p '*_test.py'
```

The core is designed around this project's worktree, manifest, service, run, and runtime-fact model.
[Product boundaries](docs/product.md) and [architecture](docs/architecture.md) describe the current contract.
Historical code provenance remains documented in [UPSTREAM](engine/UPSTREAM.md).

## License

Maintained by **SheathedSharp** and project contributors. Project code is available under the
[MIT License](LICENSE). Third-party code, fonts, and assets retain their own terms and copyright notices;
see [third-party notices](THIRD_PARTY_NOTICES.md). External agent names describe compatibility, not endorsement.
