<p align="center"><picture><source media="(prefers-color-scheme: dark)" srcset="brand/option-berth-lockup-dark.svg"><img src="brand/option-berth-lockup-light.svg" alt="option-berth" width="420"></picture></p>

# option-berth

English | [中文](README.zh.md)

Run local development services across Git worktrees. The `oberth` CLI starts and stops declared services and reports which checkout owns a run, whether its port is listening, whether health checks pass, and why it exited.

Use it for local projects with APIs, background workers or several branches running at once, including workflows driven by coding agents. It is not an OS sandbox, a production deployment platform or a machine-wide process cleaner. External services are not implicitly taken over.

## Install, then run one service

macOS / Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

See [installation](docs/install.md) for Windows, manual downloads, verification, version pinning and protocol switches. Full service lifecycle support is primarily macOS/Linux; a Windows build is not equivalent native acceptance.

Follow the [quick start](docs/quickstart.md) first: it needs only Python 3's standard library, no model account or Docker. In a project with a reviewed `oberth.yaml`, the everyday loop is:

```sh
oberth up --wait
oberth status --json
oberth logs api --once
oberth down
```

`up --wait` waits for the declared readiness conditions. `status` distinguishes a process, a listener and health; inability to observe is not “not running.” `down` targets the current worktree, not the whole machine. Replace the example service name `api` with your own.

See [product scope and safety boundaries](docs/product.md) for the complete definition.

## Choose your next task

| Task | Read |
| --- | --- |
| Run two branches at once | [Multiple worktrees](docs/worktrees.md) |
| Understand absence, idle exit or version switching | [Runtime Service](docs/runtime-service.md) |
| Diagnose startup, port or observation failures | [Troubleshooting](docs/troubleshooting.md) |
| Integrate an orchestrator or coding agent | [Integrations](docs/integrations.md), [separate skill](https://github.com/SheathedSharp/option-berth-skill) |
| Understand optional model advice and consent | [Attention](docs/attention.md) |
| Leave a structured handoff | [Work records](docs/workplace.md) |
| Look up flags, JSON, RPC or manifest fields | [Interface reference](docs/cli.md) |

The [documentation index](docs/index.md) covers the full reading path. The [macOS interface](https://github.com/SheathedSharp/option-berth-macos) is a separate project, not a CLI prerequisite; components need matching protocol versions.

## Contribute

Start with [development](docs/agent/develop.md), then [architecture](docs/agent/architecture.md) and [verification scope](docs/agent/validation.md). Follow [AGENTS.md](AGENTS.md) before editing. Tests must not use your everyday background host or real project data. Earlier test results do not establish acceptance of today's revision.

Release requirements are maintained in the [release process](docs/agent/releasing.md), not implied by a local build.

## License and provenance

Project contributions use [MIT](LICENSE). Third-party components retain their own notices: see [attribution and source](third_party/README.md) and [source archives](docs/third-party.md). Archives support inspection and development without changing Go dependency resolution. Third-party tool names do not imply endorsement.
