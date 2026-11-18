# oberth interface reference

English | [中文](cli.zh.md)

This is the lookup contract for commands, JSON, RPC and manifest fields. Start with the [quick start](quickstart.md) if you are new; this page is not intended as a linear tutorial. Query `oberth <command> --help` for the running build. Generated schemas come from the Go types.

A worktree is a checkout directory; its manifest is `oberth.yaml`; a service is a declared command; a run is one concrete invocation. Runtime Service is the local background owner of observation, run registration and storage. A process, listener and health verdict are distinct; unknown is not absence.

Use the [documentation index](index.md) to select a task. Below are the rules every command follows, the layer table, and the page that owns each layer's commands. For optional model consent, start with [Attention](attention.md).

## Reference pages

| Page | Covers |
|---|---|
| [Runtime facts](cli/facts.md) | `status`, `logs`, `events`, `worktrees` |
| [Operations](cli/operations.md) | `up`, `restart`, `down`, `start`, `kill` |
| [Code and declaration](cli/declaration.md) | `git`, `init`, the manifest boundaries and references |
| [Handoff](cli/handoff.md) | `workplace`, `attn`, `agent`, agent stream, native continuation |
| [Infrastructure](cli/infrastructure.md) | `doctor`, `runtime service`, `version`, protocol 3, `feedback`, the first screen |
| [Agent-facing JSON fields](cli/json.md) | The fields an agent branches on in `status`, `up` and `restart` output |

## Common rules

- Commands work on the current directory (or an explicit path / project name). With no manifest, run oberth init first: a command that needs the manifest answers `not_found` with that step in the hint.
- Default output is for humans; with plain `--json`, stdout holds exactly one JSON value. Explicit streaming commands emit one value per line, with errors written to stderr.
- Following logs and other streaming commands use NDJSON: one JSON value per line.
- Exit codes: 0 success, 1 run failure, 2 argument or option error, 130 interrupted by Ctrl-C.
- The JSON error shape is {"error":{"code":"...","message":"...","hint":"..."}}.
  The stable codes are invalid_params, not_found, invalid_config, ambiguous, permission_denied, runtime_service_busy, observation_empty, observation_unrecognized, observation_incomplete, observation_blind, observation_bind_denied and internal. Each one is either a refusal, a condition or a fault: a refusal means the request itself is wrong and repeating it cannot help, a condition means the world was not ready and a retry may succeed, and a fault means the engine is broken. A readiness timeout is not among them: `up --wait` reports it as that service's state, so it travels as a runtime fact rather than as a transport error. None of the observation codes mean "nothing is listening":
  - observation_empty: the collection command exited 0 but produced no output at all; a self-proof probe decides whether this is a genuinely empty table or blindness.
  - observation_unrecognized: there was output but no recognizable table header — a format problem.
  - observation_incomplete: some output lines could not be decoded. Readable lines are published as usual, the ports on bad lines stay unknown, and `observation.state` is partial; if the command as a whole fails, it is still treated as a failure.
  - observation_blind: the probe can bind a local port, but the same collector cannot see its own listener.
  - observation_bind_denied: the probe cannot even bind a local port, so services started from here cannot listen either.
  The hint points at retrying outside the sandbox, upgrading option-berth, or running
  `oberth doctor --only listener_observation` to check the current process.
- Each platform reads the listener table with an ordered chain of collectors: `netstat` then `lsof`
  on macOS, `ss` then `netstat` on Linux, and `netstat -ano` alone on Windows. The first one is the
  primary and stays unnamed in `observation.source`; another one is named only when it answered
  because the primary could not. The next collector is tried only when the pass before it left no
  usable row: the tool is not installed, it exited 0 without printing a table, or its own diagnostics
  invalidated a pass that had no readable row. Rows that were read end the round even when the pass
  failed — they are published with their verdict and are never replaced by another tool's table — and
  a table that was printed and could not be decoded is a decoder problem that is not retried either.
  When every collector fails, the primary keeps its code and hint and the other collectors' reasons
  are folded into the same `detail` sentence.
- Starting with macOS 27, a child process launched by a parent that Apple has not signed
  (Go, Homebrew Python and so on) runs `netstat` and gets exit 0 with no TCP table. That
  "not one row" case is still treated as observation_empty and the self-proof probe decides it;
  when the probe can see its own listener, `lsof` answers this round and it still reports observable.
  The `doctor` detail and the `runtime service status` `observation` describe the same round.
- A named source is narrower than the primary table, and its `detail` sentence states the limits that
  collector declares: a non-root `lsof` cannot see other users' sockets, so a port held by a root
  process may be missing from it, and it reports a dual-stack wildcard socket only as `::`, without
  adding the other address family; Linux `netstat` also reports a dual-stack wildcard socket under
  one address family. A row in the table is a fact, but absence is not: consumers read
  `observation.source` before concluding that a port is free. `machine[].listening_known` is false
  for a port missing from a narrow table, `port: auto` verifies each candidate by binding it instead
  of trusting absence, and the human `status` prints `listening unknown`.
- `runtime_service.observe_sources` (default 1) says how many rungs of that chain one scan may read.
  The default stays a single collector, whose alternative is named in `observation.source`; a larger
  N is clamped to the platform's chain length (two on macOS and Linux, one on Windows) and publishes
  `observation.sources`, the collectors that contributed, in chain order, while `observation.source`
  stays empty. A one-rung chain stays a single read however large N is: neither field is set. A
  collector that read rows contributes them and names itself on each one in `ports[].source`; one
  that ran clean and saw nothing contributes its name alone; one that failed contributes nothing, so
  a failing rung can never turn an unread port into an absent one. A row's collector is what makes
  the row weighable: the rungs cover different sockets, so the table-level `observation.sources`
  alone cannot say which table a given row came from. A merged table is always narrow — its rows are
  facts, a port missing from it is unknown rather than free, `machine[].listening_known` follows the
  same rule as for a named source, and its `detail` names the collectors it came from.
- `runtime_service.observe_primary_failures` (default 3) and
  `runtime_service.observe_primary_cooldown` (default 60s) bound how long the Runtime Service keeps
  paying for a primary collector that never works. After that many failures in a row the primary is
  not run for one cooldown and the alternatives answer every scan; when the cooldown ends exactly one
  real read of the primary is made, and that probe closes the breaker on success or reopens it on
  failure; a probe the caller withdrew learned nothing and closes nothing. A skipped primary changes
  no fact: `observation.source` or `observation.sources` still name who answered,
  `observation.degraded_since` does not move, and absence from that table stays unknown, exactly as
  it is for a named source. Its `detail` gains one sentence saying the primary did not run, how many
  failures in a row it has and when it is retried, so a reader can tell a skipped attempt from a
  collector that answered because it had recovered. A chain with a single collector is never
  skipped — there is no alternative to read instead — and
  `observe_primary_failures: 0` turns the breaker off, so every scan pays for the primary again. The
  breaker is per-process state: nothing about it is written to the database or to `oberth.yaml`, and
  a restarted Runtime Service tries the primary again.
- `observation.degraded_since` and `observation.recovered_at` are RFC 3339 times maintained by the
  Runtime Service. The first is when the published observation stopped being a whole read of the
  primary table; the second is when a healthy run began again after such a degradation, and it stays
  until the next one. `checked_at` is only the moment of the latest check, never the start of a
  degradation. A direct `--no-runtime-service` read has no history, so it leaves both empty.

- --no-runtime-service makes fact reads collect locally without connecting to or starting a Runtime Service; operations that record or start/stop runs still need the host. Historical records use the same database read-only while it is offline. If an active owner holds it, the direct read returns `runtime_service_busy`, not empty history; omit the flag for online reads through the owning host.
- No wire field is accepted without being read. The protocol schema is generated from the runtime service's Go types, so a field declared there is a promise every client may rely on; a field no handler acts on is worse than a missing one, because the call succeeds and the caller cannot tell it was ignored. Every field a method's params can carry is therefore read by this repository's own code, and `internal/runtimesvc/rpc` fails the build otherwise. Withdrawing a published field is a protocol change; the names removed on purpose are recorded in that package so they cannot return silently.

## Command surface

The CLI is layered by **what an agent wants to do**, not by internal modules: facts are "what state is
it in now", operations are "make it run or stop", code and declaration are "read the code and the
manifest", handoff is "leave or retrieve work, hand the session to an external agent", and
infrastructure is the runtime environment itself. The root help groups by these five layers; a new
product capability may only land in an existing layer, and may no longer stuff one action into another
command's options.

| Layer | Public commands | Boundary |
|---|---|---|
| Facts | `status` · `logs` · `events` · `worktrees` | The current project's declarations, runtime facts and changes; checkout identities this machine has seen |
| Operations | `up` · `restart` · `down` · `start` · `kill` | Start and stop by manifest, or explicitly name a low-level process entry point |
| Code and declaration | `git` · `init` | Read-only code context, and this worktree's manifest |
| Handoff | `workplace` · `agent` · `attn` | Work records, handing the session to an external coding agent, and the optional model assessment (off by default) |
| Infrastructure | `runtime` · `config` · `version` · `completion` · `doctor` · `feedback` | Background collector, install configuration, self-check and optional improvement reports |

`oberth --help` ends with a **For agents** section that gives a reading pattern, not commands: the
three reads a session opens with (`status --json --brief`, `up --wait --json`,
`logs <service> --once --json --run current --first-error`). It adds no command and does not change
the layer table above; the `--help` of `status`, `up` and `logs` carries the reasoning behind the
same reads.

<a id="core-loop"></a>
