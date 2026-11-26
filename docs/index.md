# Documentation

English | [中文](index.zh.md)

Pick the task you have; the protocol can wait. `oberth` runs the services a local Git worktree declares and then publishes what it can see — processes, ports, health, exits, and a diagnosis of anything that is wrong.

## Use oberth

| You want to… | Read |
| --- | --- |
| Install or upgrade | [Installation](install.md) |
| Run a first service | [Quick start](quickstart.md) |
| Work on two branches at once | [Multiple worktrees](worktrees.md) |
| Understand the background host | [Runtime Service](runtime-service.md) |
| See the whole runtime picture and why a service failed | [Runtime facts](cli/facts.md#oberth-runtime-inspect-path---json) |
| Diagnose a failure step by step | [Troubleshooting](troubleshooting.md) |
| Read attention notes, or turn on model advice | [Attention](attention.md) |
| Give an agent the operating instructions | [Integrations](integrations.md) |
| Record a temporary task or hand work over | [Work records](workplace.md) |

## Look up a contract

[CLI, JSON, RPC and manifest reference](cli.md) is a lookup document, not a tutorial. [Product scope](product.md) explains responsibilities and exclusions; [open work](backlog.md) separates supported behavior from missing acceptance evidence. Generated schemas live in [engine/docs/schema](../engine/docs/schema/); edit their Go sources and regenerate them, not the JSON by hand.

## Develop or extend the project

Start with [development](agent/develop.md), then [architecture](agent/architecture.md) for package boundaries and [verification](agent/validation.md) for the right checks. [Release process](agent/releasing.md) governs publishing, not the quick start. [Third-party sources](third-party.md) explains the source archives and license checks.

Engineering pages under `agent/` are English-only. Dated material under `agent/history/` records earlier inputs and results; it is not a test pass for today's revision. The [writing rules](AGENTS.md) govern changes to these pages.
