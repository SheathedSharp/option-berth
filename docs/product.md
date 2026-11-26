# What oberth is responsible for

English | [中文](product.zh.md)

`oberth` is a local service lifecycle and observation tool for Git worktrees. A person and a coding agent read the same CLI/JSON interface, so neither has to guess which checkout owns a process. Start with the [quick start](quickstart.md), or use the [reference](cli.md) for exact behavior.

## The objects it manages

| Object | Meaning | Authority |
| --- | --- | --- |
| Worktree | A checkout directory and its Git identity | Code and filesystem observation |
| Manifest | `oberth.yaml`, the intended services | A person reviews and adopts it |
| Service | A command, dependencies, optional port and readiness checks | The manifest |
| Run | One concrete invocation with identity, PID, start/end and logs | The runtime records it |
| Runtime facts | Observed process, listener, health, exits and changes | Deterministic code, not model inference |
| Work record | A temporary task and its observed outcome | Explicit commands and recorded exits |

A live process, an open port and a healthy application are three different facts. A missing observation is unknown, not proof of absence. A run stays attached to its worktree and its generation; a delayed observation cannot be reassigned to the replacement run.

## One Runtime World

Those objects meet in a single reduced document, the Runtime World (`oberth.runtime.world/v1`), which `oberth runtime inspect` prints and the Runtime Service also serves over RPC. Its parts are produced in one direction:

- **Intent** — `oberth.yaml`, reviewed and adopted by a person.
- **Execution** — the runs, ports and generations the engine started and still owns.
- **Observation** — process, listener, health and code facts read from this machine, carrying an explicit coverage.
- **Causal diagnosis** — root causes, drifts, loops and next actions reduced from the three above by deterministic code.

Coverage is part of the answer: `observable`, `partial` or `blind` says how much of the machine this round actually saw, and a blind round never becomes "nothing is running". Health summarizes the worktree; each service carries a state such as `ready`, `crash_loop`, `port_conflict` or `blocked_by_dep`. When something is wrong, `diagnosis` names the root cause with its evidence and offers next actions ranked by safety: `read_only`, `local_restart` or `human_decision`. The same diagnosis arrives additively in `oberth status`, so an existing consumer keeps working.

`oberth status` remains the per-command read; the Runtime World is the single reduction they share. Neither is a second source of truth for the other.

## What you can do

Declare services, start them in dependency order, wait for readiness, read logs and stop only the selected worktree's runs. Automatic ports are reservations, not permission to take an occupied fixed port. Portless workers are valid services. A deleted checkout can still be addressed by its recorded identity; application data is never automatically deleted.

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

Cancellation, failure and partial progress stay distinct. A successful stop request is not proof of exit; failures preserve per-item receipts. Recovery validates records before publishing them. Implementation boundaries belong to [architecture](agent/architecture.md), not to a second state cache.

## Attention is optional, and layered

Two layers share the name. Attention Notes are deterministic: they are derived from the Runtime World, stored owner-only on this machine, and computed with no key, no network call and no consent. Above them sits an opt-in model triage that can judge whether an anomaly matters to the current task.

The triage runs in one of three modes. `strict` keeps it to facts and evidence. `assist` adds urgency, relevance, a bounded summary and one recommended fixed option. `reconcile` may additionally mark a note eligible for an automatic local restart, and only when every gate holds: a restarted local service, low uncertainty, high confidence, no crash loop and no foreign port holder. Even then it does not kill a process it did not start and does not edit a manifest. Model output can select only from the published option vocabulary; it cannot produce a command or widen authority. See [Attention](attention.md).

## Agents and interfaces

The CLI's human output and JSON describe the same state. Coding assistants learn the operating rules from the [separate skill](https://github.com/SheathedSharp/option-berth-skill); orchestrators call explicit setup/run/cleanup commands as described in [integrations](integrations.md). oberth does not install their hooks, register MCP integrations or edit their settings.

External agent sessions remain the external provider's responsibility: login, model, approvals, tool execution and restoration are not reimplemented here. Messages use exact arguments or native terminal input, not hidden shell interpolation. Oberth never auto-approves, weakens provider permissions or restores another worktree's last session. Available providers are reported by `agent list --json`, not promised from a static list.

## What this does not promise

It is not OS-level isolation: service commands run with the user's permissions and can reach shared files, databases and containers. It is not a production scheduler, remote-host manager, public port-sharing service, whole-machine radar or autonomous cleanup agent.

Attention can judge task relevance but cannot establish process truth or authorize side effects. The engine stays usable without keys, network access or a model. [Open work](backlog.md) and [verification scope](agent/validation.md) limit claims to evidence actually obtained.
