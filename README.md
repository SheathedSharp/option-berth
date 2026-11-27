<p align="center"><picture><source media="(prefers-color-scheme: dark)" srcset="brand/option-berth-lockup-dark.svg"><img src="brand/option-berth-lockup-light.svg" alt="option-berth" width="420"></picture></p>

# option-berth

English | [中文](README.zh.md)

Run the services a Git worktree declares, then read what is true about them. `oberth` starts and stops those services and publishes one picture of the worktree: which checkout owns a run, which port it listens on, whether health passes, why it exited, and what to do next.

It fits local projects running APIs, background workers or several branches at once, including work driven by coding agents. It is not an OS sandbox, a production scheduler or a machine-wide process cleaner, and it never takes over a service you did not declare.

## Install and run one service

macOS / Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

Windows, manual downloads, verification, version pinning and protocol switches are in [installation](docs/install.md). Full service lifecycle support targets macOS and Linux; a passing Windows build is not the same native acceptance.

The [quick start](docs/quickstart.md) needs only Python 3's standard library — no model account, no Docker. In a project with a reviewed `oberth.yaml`:

```sh
oberth up --wait
oberth status --json
oberth logs api --once
oberth down
```

`up --wait` returns once the declared readiness conditions hold. `status` keeps the process, the listener and the application's health apart, and reports "cannot observe" as unknown instead of "not running". `down` stops this worktree, not the machine. Replace `api` with your own service name; the [product page](docs/product.md) holds the full scope.

## Read the Runtime World

`oberth runtime inspect` reduces the current facts about a worktree into one document with a fixed `schema` (`oberth.runtime.world/v1`) and a deterministic `revision` that ignores the clock:

```sh
oberth runtime inspect --json
```

`coverage` says how much this observation actually saw — `observable`, `partial` or `blind` — and `health` summarizes the worktree. Every service carries a `state` such as `ready`, `crash_loop`, `port_conflict` or `blocked_by_dep`, together with `blocked_by`, `drifts` and `first_error`. When something is wrong, `diagnosis` says why: `root_causes` with their evidence, the `drifts` and `loops` behind them, and `next_actions` ordered by `safety` — `read_only`, `local_restart` or `human_decision`. `oberth status` and `status --brief` carry the same `diagnosis` as an additive field, so existing consumers keep working.

## Attention, when you want it

Attention Notes are deterministic and always available: `oberth attn notes` derives them from the Runtime World, stores them owner-only under `BERTH_HOME`, and needs no key, no network and no consent.

```sh
oberth attn notes
oberth attn on
```

`oberth attn on` is the separate opt-in that adds model triage on top: `strict`, `assist` or `reconcile`. `reconcile` can only mark a restart-bearing note eligible — it never kills a process it did not start and never edits a manifest. [Attention](docs/attention.md) owns the consent rules and what leaves the machine.

## Choose your next task

| Task | Read |
| --- | --- |
| Run two branches at once | [Multiple worktrees](docs/worktrees.md) |
| Understand an absent host, idle exit or version switch | [Runtime Service](docs/runtime-service.md) |
| Diagnose a startup, port or observation failure | [Troubleshooting](docs/troubleshooting.md) |
| Integrate an orchestrator or a coding agent | [Integrations](docs/integrations.md), [separate skill](https://github.com/SheathedSharp/option-berth-skill) |
| Understand model advice, notes and consent | [Attention](docs/attention.md) |
| Leave a structured handoff | [Work records](docs/workplace.md) |
| Look up flags, JSON, RPC or manifest fields | [Interface reference](docs/cli.md) |

The [documentation index](docs/index.md) maps the whole reading path. Components talk to each other only across matching protocol versions.

## Contribute

Start with [development](docs/agent/develop.md), then [architecture](docs/agent/architecture.md) and [verification scope](docs/agent/validation.md). Read [AGENTS.md](AGENTS.md) before editing; tests must not touch your everyday background host or real project data, and an older green run says nothing about today's revision.

Release requirements live in the [release process](docs/agent/releasing.md); a local build passing does not imply one.

## License and provenance

Contributions to this project are [MIT](LICENSE). Third-party components keep their own notices: see [attribution and source](third_party/README.md) and [source archives](docs/third-party.md). The archives support inspection and development without changing Go dependency resolution. A third-party tool's name here describes compatibility, never endorsement.
