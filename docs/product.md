# What oberth is responsible for

English | [中文](product.zh.md)

`oberth` is a local service lifecycle and observation tool for Git worktrees. A person and a coding agent read the same CLI/JSON interface; neither needs a separate model of which checkout owns a process. Start with the [quick start](quickstart.md), or use the [reference](cli.md) for exact behavior.

## The objects it manages

| Object | Meaning | Authority |
| --- | --- | --- |
| Worktree | A checkout directory and its Git identity | Code and filesystem observation |
| Manifest | `oberth.yaml`, the intended services | A person reviews and adopts it |
| Service | A command, dependencies, optional port and readiness checks | The manifest |
| Run | One concrete invocation with identity, PID, start/end and logs | The runtime records it |
| Runtime facts | Observed process, listener, health, exits and changes | Deterministic code, not model inference |
| Work record | A temporary task and its observed outcome | Explicit commands and recorded exits |

A live process, an open port and a healthy application are different facts. A missing observation is unknown, not proof of absence. A run remains attached to its worktree and generation; delayed observations cannot be reassigned to a replacement run.

## What you can do

Declare services, start them in dependency order, wait for readiness, read logs and stop only the selected worktree's runs. Automatic ports are reservations, not permission to take an occupied fixed port. No-port workers are valid services. A deleted checkout can still be addressed by its recorded identity; application data is never automatically deleted.

“Oberth Runtime” names this domain. “Runtime Service” is the local background host that owns the live registry, observation and storage. It is not a second product or a project service; see [background service](runtime-service.md).

## Who may change what

| Action | Boundary |
| --- | --- |
| Edit the manifest | A person confirms it; an agent may draft, but drafting is not adoption |
| Start/stop/restart | Only the selected declared services or explicitly named low-level target; verify identity before signalling |
| Observe `machine:` dependencies | Read-only; never include them in project start/stop targets |
| Read `workspace:` addresses | Explicit relations only; an address reference does not grant lifecycle control |
| Use Git context | Product Git operations are read-only |
| Use a model | Optional, separately consented advice; never a prerequisite for control |
| Send feedback | Opt-in with the stated collection and redaction boundaries |

Cancellation, failure and partial progress remain distinct. A successful stop request is not proof of exit; failures preserve per-item receipts. Recovery validates records before publishing them. Implementation boundaries belong to [architecture](agent/architecture.md), not a second state cache.

## Agents and interfaces

The CLI's human output and JSON describe the same state. Coding assistants learn the operating rules from the [separate skill](https://github.com/SheathedSharp/option-berth-skill); orchestrators call explicit setup/run/cleanup commands as described in [integrations](integrations.md). oberth does not install their hooks, register MCP integrations or edit their settings.

External agent sessions remain the external provider's responsibility: login, model, approvals, tool execution and restoration are not reimplemented here. Messages use exact arguments or native terminal input, not hidden shell interpolation. Oberth never auto-approves, weakens provider permissions or restores another worktree's last session. Available providers are reported by `agent list --json`, not promised from a static marketing list.

## What this does not promise

It is not OS-level isolation: service commands run with the user's permissions and can access shared files, databases and containers. It is not a production scheduler, remote-host manager, public port-sharing service, whole-machine radar or autonomous cleanup agent.

Optional [Attention](attention.md) can judge task relevance but cannot establish process truth or authorize side effects. The engine stays usable without keys, network access or a model. [Open work](backlog.md) and [verification scope](agent/validation.md) limit claims to evidence actually obtained.
