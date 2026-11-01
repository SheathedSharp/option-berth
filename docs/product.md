# option-berth · Product definition

English | [中文](product.zh.md)

The single product definition for this repository. Implementation and support boundaries are in
[`architecture.md`](agent/architecture.md); current plans are in [`backlog.md`](backlog.md).

## The need

> When you let agents write code across several worktrees at once, anyone holding any one checkout
> should be able to answer, with **one command**: **what should this run, is it really running, and if
> not, why** — and then actually start it and stop it.

The through-line is **what a coding agent can do**, not what a UI can do: an agent should not have to
guess a port, page through the process table, or come back and ask a person "is your service up?" to
judge its own runtime environment, and it should not hand "why didn't it start" back to the user to
investigate. A person reads the same answer.

The client is one presentation of that answer, not the product's through-line and not a second source
of facts (see "Two readers, one interface").

Terminal sessions and external coding-agent sessions revolve around the development work on this
worktree; they do not change where the core runtime facts come from.

## Position

option-berth is **the runtime facts layer for worktrees**.

Orchestration tools create workspaces, schedule sessions and often allocate their own ports;
option-berth owns the part they most easily get wrong and can least easily patch themselves. The
following are not add-on features but this chain of facts' product promises:

- Process, listener and health are three separate facts, and a failure leaves a stable reason code;
- A run belongs to the worktree that started it: stopping never crosses into another checkout and never
  hits someone else's service;
- When a sandbox or permissions break observation, it says plainly "I cannot see" rather than writing
  unknown as "not running";
- After a checkout directory is deleted, its leftover services can still be stopped by identity and its
  `port: auto` reservation returned.

Lighter tools already cover "one port per worktree"; port allocation is not this product's competitive
point, because a port is one item of runtime fact. Integration means **being called by the orchestration
flow** — the user calls `oberth` inside the tool's own setup / run / cleanup script or an outer wrapper,
and the product installs no hooks, registers no MCP server and modifies no tool's configuration.
Recipes are in [`integrations.md`](integrations.md).

The only entry points remain the CLI and `--json`: orchestration tools consume the same facts, and there
is no separate integration protocol.

## Design principles

The product is designed from its own requirements; the core algorithms and low-level implementation
follow this document's object model, write boundaries and CLI contract. Reference projects such as Sonar
offer ideas and engineering experience; they do not decide this project's architecture and are not the
skeleton for later work. No object or capability unrelated to this product is kept merely to stay
faithful to a reference design.

Self-authored replacements advance behind verifiable module boundaries, keep the existing external
contract, and are proven by behavior, isolation and performance — not by a rename or a new wrapper
layer. Origins are recorded honestly in [`engine/UPSTREAM.md`](../engine/UPSTREAM.md); migration status
is tracked only in [`backlog.md`](backlog.md).

## Object model

There is one chain, and each level states who produces it:

```text
Worktree      path + git identity (repository, branch, dirty count)      code observes
 ├─ Manifest  oberth.yaml                                                a person writes (agent may draft, person adopts)
 │   └─ Service   one service: prepare / cmd / port / depends_on         a person writes
 │       └─ Run       one run: pid, start/end, exit code, log path       the background process records
 │           └─ Runtime facts  alive, listening port, exit reason        code observes
 ├─ Workplace   work records on this worktree: workplaces and their blocks   engine records (front end may report)
+ Undeclared  listeners whose cwd is in this worktree but absent from the manifest   code observes
```

- **A listening port is a Run's live fact, not an independent entity.** A normal port displays with its
  service; only an undeclared listener, a port conflict or unclear attribution is surfaced separately.
  The product does not answer "what is every port on this machine".
- A **`machine:` reference** is a top-level manifest section: a service this project depends on but does
  not start or stop (database, middleware). It only shows "listening / not listening"; `up` / `down`
  never touch it.
- A **`workspace:` reference** is read-only too: it points at a service in another checkout and supplies
  only addresses such as `${<name>.url}` (including that side's `port: auto`); `up` / `down` /
  `restart` never start or stop the other side. Addresses are read only from the counterpart manifest
  and published runtime facts, never from a separate state source.
- A service may carry a `prepare:` pre-command. `up` and `restart` complete it before launching the
  long-running `cmd:`, which suits a Java jar, generated code or any artifact that must be reproduced
  from the current checkout; a failing pre-command means no service is started. `prepare_inputs:` lists
  the files it reads, and a second `up` skips the step while their content is unchanged (a Node project
  does not reinstall dependencies).
- `sources:` declares the files or directories used to tell whether the process is running the current
  code (default: the service directory). Start records a content fingerprint and the HEAD at that
  moment; `status` recomputes and publishes `code_changed_since_start`, the number of changed files and
  a restart hint. An explicit `sources: []` turns the check off, which suits a service that hot-reloads
  its own code.
- A service may reference a dotenv file read-only through `env_file:`. Relative paths resolve against
  the manifest directory, so several worktrees can share one configuration and its secrets outside the
  checkout; the file is read only at start, and its content never enters state, logs or a hash.
- A health check is part of the service declaration: an HTTP path, a TCP connection or a command. A
  service like a database — where the port opens before the protocol is ready — uses a command check, so
  dependents and `up --wait` continue only once it is genuinely connectable.
- When two worktrees run the same repository at once, each Run belongs to the worktree that started it
  and is never misattributed. A data directory or database name can be separated by the manifest's
  stable `${worktree}` identity, so the two need not share one dataset.
- **The closing end of a worktree is part of the chain of facts too.** The current identity is published
  with `status --json`; `worktrees` lists identities the engine has seen and checkouts that no longer
  exist, so a person can clean up old databases and data directories; `down --worktree/--identity` is
  the explicit entry point that still stops a service and returns its reservation after the worktree
  directory was deleted. The engine only reports and stops; it never deletes a person's data, and it
  refuses a group-scoped stop when two checkouts share one group name rather than widening the target.
- **Work records hang off the worktree, not the service chain.** A worktree can hold several workplaces,
  each keeping its work entries in order: commands the engine ran and each round of work handed to an
  agent. It is the record of "what happened here and how did it turn out" — an agent can append to it
  itself and can read it back to take over someone else's work (or its own from a previous round). It
  is not a new runtime truth: entries the engine records point at the corresponding Run and log, and an
  entry with no observed end stays running rather than posing as success. The product never judges
  service state from it and never treats it as session restoration.

## The core loop

An agent working on a worktree goes back and forth through these:

```text
oberth status --json     what the state is: what is declared, what runs, why it last failed, which code
oberth up --json         make it run (depends_on order, already-running ones skipped)
oberth logs --json       it did not start or started wrong: its own output and exit reason
oberth workplace show    what was done here before, and how it turned out
```

A person reads the table from the same commands; the loop is the same set of actions:

```text
cd <worktree>
oberth status    what is declared, what actually runs, why it last failed, which code
oberth up        start services per the manifest (depends_on order, already-running ones skipped; --only includes the dependency closure by default)
oberth restart   restart every service in the project, or only the named ones with --only
oberth logs      straight from a service to its log, exit code and last output
oberth down      stop every service in this project
```

With no manifest, `oberth init` writes a starter. When an agent's help is wanted, `oberth init draft`
produces a draft and a person adopts it to disk with `oberth init adopt`.

## Command layers

Root help groups by **what an agent wants to do**, not by internal modules. New product capabilities
may only land in an existing layer:

| Layer | Commands | What this layer answers |
|---|---|---|
| Facts | `status` · `logs` · `events` · `worktrees` | What the state is and what is changing; which checkout identities this machine has seen |
| Operations | `up` · `restart` · `down` · `start` · `kill` | Make it run, make it stop |
| Code and declaration | `git` · `init` | Code context, and this worktree's manifest |
| Handoff | `workplace` · `agent` | Leave and retrieve work records; hand a session to an external agent |
| Infrastructure | `daemon` · `serve` · `config` · `version` · `completion` · `doctor` | The runtime environment, configuration and self-checks |

`events` only subscribes read-only to state changes of explicitly related worktrees; `start` runs a
single command, and `kill` handles only explicitly named processes. `agent list/plan/run` is the
explicit launch adapter for external coding agents; it implements no model, tool loop or session
reasoning system.

## Two readers, one interface

**The interface is the CLI only.** An agent reads `--json`; a person reads the table from the same
command. `status --json`'s top-level object is `worktree`. The client is a presentation of the same
state and grows no capability the CLI lacks.

The agent skill is an independent product:
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill). It teaches an agent the same
CLI and `--json`, but is not distributed from this repository. When a command, a `--json` shape, an
error code, a manifest field, a reference or a `--json` field an agent reads changes,
`scripts/check_skill_contract.py` checks against `docs/cli.md`, the manifest schema and the observation
constants whether the skill covers it. Whether to install it is the user's decision.

`oberth events` is the read-only NDJSON subscription entry point for agents developing in parallel. It
supports filtering by worktree root path or ID (including same-repository worktrees along existing Repo
relations), by repository ID and by an explicit workspace relation. The first record is a summary
snapshot and later ones are `state.changed` summaries; it forwards existing runtime facts only, never
broadcasting logs, commands, working directories or environment variables, and it performs no actions.

When the code observes a definite anomaly, `status --json` may also publish a derived attention file
under `BERTH_HOME` so that a development agent using oberth can decide whether to hand the choice back
to the user. It only cites state and log evidence; it changes no fact and performs no action. If Jev is
used through an external adapter it only adds an escalation suggestion and never enters the engine's
control path. How the client stores that optional adapter's local configuration is the client
repository's contract.

## Write boundaries

| Object | What option-berth may do |
|---|---|
| Code / git | Read-only: no commit, no stage, no branch switch |
| `oberth.yaml` | Read, validate, draft; writing to disk only follows a person's adoption |
| Project services | Start and stop per the manifest, read logs |
| Work records | Record work the engine started and entries a front end reports; add and remove workplaces and their blocks. Never start, restore, or observe a front end's own terminal for it |
| Machine-level services | Only observe whether they listen; `up` / `down` never touch them, and a named `kill` is the only exception |
| Another worktree's services | Read-only addresses: never start, never stop, and never establish `depends_on` |
| Shared environment files | Read-only: read `env_file:` at start, never create or modify |
| Other people's configuration | Never write: no hooks, no MCP registration, no changes to an agent's or orchestrator's configuration. Integration recipes are documentation the user pastes, not something the product writes |

## Explicitly not done

- **A whole-machine port radar**: every port, system ports, four-way classification, a machine service
  registry. `events` must carry a worktree, repository or registered-workspace filter; an unfiltered
  stream from an old RPC client is only a compatibility view and provides no new whole-machine port
  page.
- **No model judgement in the core**: "why didn't it start" and "whose is this" are never handed to the
  daemon to infer; facts are observed only by code. An optional external attention adapter may decide
  from those facts when to hand the choice back to the user, but it cannot rewrite state or perform
  actions.
- **No extra integration layer**: no MCP server, no automatic skill install, no hooks. Integration
  recipes give only commands and script fragments for the user to paste into their own tool; the
  product distributes no integration code.
- **The client is not the product's through-line**: orchestration, model judgement and cloud sessions
  are not moved into the client. It presents CLI facts, grows no capability the CLI lacks, and is not a
  second state source.
- Guessing a start command automatically and writing it to disk; the product's own Git writes and commit
  workflow. A shell or external agent the user explicitly runs may modify code under its own
  permissions — that does not give the engine those write permissions.
- Remote hosts, public sharing, port mapping.

## Success criteria

1. An agent runs one `oberth status --json` in a real project directory and knows whether the service
   should run, whether it is running, and which line to read when it failed — without asking a person
   and without assembling `lsof` / `ps` / `tail` itself.
2. The agent starts, stops and restarts by itself; the runtime facts, process and log in the service row
   let it check its own conclusion.
3. With two worktrees running at once, no service is matched to the wrong code.
4. What was done on this worktree and how it turned out can be read back — neither a person taking over
   nor an agent has to guess from scratch.

## External session boundaries

A native terminal carries only the shell or existing coding agent the user chose. Messages travel as
exact arguments or native terminal input, never assembled into a hidden shell command; login, model,
approval, tool calls and conversation restoration are the external agent's own responsibility. The
product does not install an agent, modify its configuration, disable a sandbox or restore the latest
session from another worktree. For providers with a structured protocol (`oberth agent stream`: this
build covers Codex, Claude Code, OpenCode and Pi — see the `stream` field of `agent list --json`), the
bridge forwards approval requests and a person decides to allow or deny — presented in the
[macOS client](https://github.com/SheathedSharp/option-berth-macos), with execution still the
provider's job. The bridge passes no permission-relaxing arguments and never auto-approves. Shell
integration (zsh/bash/fish, optional) is likewise implemented by the client.
