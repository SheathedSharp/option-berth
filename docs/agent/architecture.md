# Architecture and support boundaries

Product definition: [product.md](../product.md). User interface: [cli.md](../cli.md).
This document explains the current production implementation; it does not treat past decisions,
temporary debugging or research tasks as standing requirements.

## One chain of facts

`worktree → manifest → service → run → runtime facts`

| Stage | Production responsibility | Boundary it must hold |
|---|---|---|
| Service facts | `servicefacts` pure reduction; `runsreg` supplies one capture; `groups` publishes the adaptation | No model judgement, no second persisted runtime truth; registering a run is not listener readiness. `port_holder` is a read-time join between an exit fact and the same listener batch; it covers only the failed service's own port, is never persisted and is not a second state source. Absence means not observed, not port-free. |
| Collection | `ports` calls the host's mature adapters, parses listeners and process identity | Failure, cancellation and incomplete scans with diagnostics cannot become "free"; a missing identity cannot widen a stop target. |
| Attribution | `groups` attributes by this round's path observation, manifest-content index and run identity | Two worktrees of the same repository never cross services; a missing path, an invalid manifest and unknown facts cannot pose as certain attribution. |
| Lifecycle | `groupstart` starts services in dependency order; `killer` verifies identity before signalling; the registry records exits | A failed prepare starts nothing; prepare is skipped only when the last attempt succeeded and `prepare_inputs` content is unchanged; cancellation reclaims only the work it created; a successful signal does not mean the process exited. |
| Scheduling | `scanner` owns observation, invalidation, statistics and ordered publication | An older result never overwrites newer facts; cancellation is not success; snapshots are immutable and continuation is bounded; a failed or timed-out health check (HTTP, TCP or command) is not readiness. |
| Persistence | `store`'s SQLite transactions; `runs` compatibility mirror and recovery | A required recovery failure blocks the service; updates follow the run generation; write failures are not swallowed and the code never claims it can undo OS side effects. |

Code identity rides this chain but not the scan hot path: before spawning, `groupstart` captures one
content fingerprint and HEAD for each path in `sources:`, writing them under `$BERTH_HOME/code` by run
id and deleting them when the run ends or start cleanup runs. `status` recomputes and compares only for
running services. The snapshot is this run's input evidence, not a second runtime fact: missing means
unknown, not "unchanged".

The start attempt of `groups.start` is transient diagnostic fact on this chain, not a run.
`groupstart` keeps `start_id`, phase, `started_at`, the effective `ready_timeout`, `waiting_on` and the
previous `last_prepare_ms` in memory, keyed by group + service; `state.snapshot` / `groups.list` join it
into the service row's `starting` on the returned copy only. The cached snapshot stays attempt-free, so
deltas never publish it and later reads never mistake it for a run. Dependency polling maintains
`waiting_on`: each observation writes back the dependency names still not ready, and a failed
observation keeps the previous set. It is therefore always transient diagnostic fact, never a run and
never an input to any decision. Ownership and cleanup live inside the start call: already-running
creates no attempt; dependency failure, prepare failure, spawn failure, cancellation and success (once
the run is registered) all delete it. It never enters the run registry, never touches disk, never counts
as running, never affects group status, and disappears when the daemon restarts. The prepare-duration
sidecar serves diagnostic reads only and never decides whether anything runs or is skipped.

The core is driven by this project's own requirements; databases, syscalls and platform adapters may
keep mature implementations, and nothing is rewritten for the sake of a "self-authored ratio". Origins
and copyright stay separate in [engine/UPSTREAM.md](../../engine/UPSTREAM.md) and the licenses.

The closing end of a worktree also rides this chain: `${worktree}` derives from the checkout root path,
and the daemon records `identity → root path → most recent group name / claim key` in
`worktree_identities` (migration 010). That is an identity ledger, not a second runtime fact: rows
survive directory deletion and `exists` is decided by the reading side against the filesystem. `status`
publishes only the current identity, `worktrees` only reads and enumerates, and only
`down --worktree/--identity` uses the ledger's group name to stop services and hand reservations back.
Deleting data remains a person's job.

## Optional enrichment stays off the control path

`internal/attention` holds only the deterministic part: fact input, event classification, derivation,
atomic publication, expiry and reads. It does not open a network connection, read a key or call a model.
`internal/jev` holds the optional enrichment: the Jev client, local settings, telemetry, preflight and
the runner.

`internal/cmd/attention_boundary_test.go` checks that boundary instead of leaving it as a comment:

- `go list -deps` asserts that the dependency closure of `daemon`, `scanner`, `groups`, `ports` and
  `servicefacts` contains no `internal/jev`;
- `go/parser` asserts that the imports of `status`, `up`, `down`, `restart`, `logs` and `start`
  contain none either.

So with no key, no `jev.json` and an unreachable provider, the control path still works: nothing
leaves the machine, and no service start, stop or readiness decision depends on the enrichment.

The artifact itself is gated on consent. `status` publishes it only once the person has enabled Jev,
which makes `attention_path` a signal an agent can act on directly: present means the enhancement is
on; absent means either nothing needs assessing or the user never opted in, and the agent no longer
has to tell those apart by reading the settings file. An install that turns the enhancement off also
clears what was published before, so a stale artifact cannot keep inviting an assessment the person
declined.

## Read-only addresses and shared environment

`workspace:` references create no new runtime facts, exactly like `machine:`. Addresses resolve only
from the counterpart manifest's declaration and the daemon's published runtime facts
(`state.snapshot` and the run registry); a resolution failure fails the service that uses it rather
than falling back to a literal or another cache. The targets of `up` / `down` / `restart` still come
only from this manifest's `services:`; a reference never widens them.

`oberth start`'s address expansion reuses the same manifest and the same runtime snapshot. It rewrites
command arguments only: it injects no environment on the command's behalf and writes no new state.
`env_file:` is read before every start (including `prepare:`); its contents never enter state, logs or
`spec_hash`, and a read failure blocks that service's start. Both follow "one chain of facts": no second
state source, and no cached second copy of runtime results to route around a failure path.

## Work records

`workplace` is the handoff layer's capability: an agent can read back "what happened on this worktree
and how did it turn out" to take over, and can write in attempts the engine could not see. It hangs off
the worktree, not the service chain, does not participate in service-fact reduction, and is never
treated by `status` as runtime truth. Implementation: `internal/workplace` (rules, selectors and RPC),
`internal/store/workplaces.go` (rows and migration 009) and `cmd/workplace*` (CLI and record wiring).
The command and field contract is [workplace.md](../workplace.md).

- **One owner writes.** The daemon is the ledger's only writer; when the daemon is absent the CLI's
  read path opens the same database read-only and write commands go through RPC. Work records never
  start a daemon for themselves.
- **A block closes only on an observed end.** An attached `oberth start` is closed by the process that
  waits for it; a detached run is closed by the daemon when it records that run's exit
  (`daemon.OnRunExit`, outside the registry lock); `agent stream` closes by the outcome of the
  `turn.completed` the bridge received. An entry with no end evidence stays `running` and never poses
  as success.
- **A second observation of the same id is an update, not a second entry.** A recorded result is never
  overwritten by a later `running`, and empty fields do not clear content already written, so a
  piece of work's start and end may arrive in any order; `seq` is fixed by the first observation.
- **Logs are not copied.** A block the engine records carries only `run_id` and `log_path`; only a
  front-end-reported block (`source: reported`) carries text output, explicitly marked as the
  reporter's words rather than the engine's evidence.
- **A record failure is diagnostic, not a command failure.** Recording lands asynchronously outside
  `start` and `agent stream`, states itself once on stderr, and is dropped with a note when the queue is
  full, because a silently lost record reads exactly like a complete one.

## Start, stop and port reservations

A start accepts only readiness evidence for the current service. The no-port run's ID/PID/StartedAt and
the stop receipt are fixed before the first signal and are not re-derived from the same PID in a later
stage. Operations are refused when reliable control identity is missing.

`down` captures reservations before signalling, reuses a fresh scan after the actual stop, checks the
original registry (including compatibility stopping records), and deletes only after comparing the full
reservation set inside a short transaction. Failure, cancellation, partial stops, remaining runs or
listeners and changed reservations all keep the reservations and report plainly. The empty-target
branch is checked the same way; `not_found` and `envelope.OK` are not substituted for evidence.

A constant-size revision per Store invalidates stale release observations; the final transaction's
comparison prevents deleting a row that already changed. No per-historical-task state is kept. No
database transaction or claims lock is held across a stop; final writes order as
claimsMu → runtime.runsMu read lock → registry.mirrorMu → Store.wmu, and the metadata mu never spans IO.
A lock conflict keeps the reservation and prompts a fresh observation and retry. Callbacks must not
re-enter the scanner, the claims manager or registry writes.

## Scope of support

The maintenance target is local development services, with macOS as the primary environment and Linux
as the engine's verification environment. Windows verifies x64 native builds, CLI smoke tests and
fact/persistence tests on hosted runners, and cross-builds arm64; these checks are not equivalent to
full service start/stop or GUI acceptance on Windows.

Each BERTH_HOME uses a single writing instance governed by a daemon lock; upgrades stop the old
instance first. The following are unsupported: old writers that ignore the lock protocol, multiple
daemons forcibly sharing a ledger, and external direct database writes. Compatibility code must never
be advertised as "old writers may write concurrently".

Ordinary checkouts, regular linked worktrees and explicit manifest paths are supported. Bare
repositories, common-dir setups and isolated Git directories are not promised in this round; when
attribution cannot be confirmed, keep it unknown and refuse dangerous control rather than guessing.
Platform permissions decide how much process evidence is visible; this is not a whole-machine process
radar, and privileges are never raised to observe other users.

OS observation and database commit are not one atomic transaction: an external process may change
between them. Numeric-PID signalling keeps a system-level race window; in-flight IO in a synchronized
filesystem, platform call or database driver is not guaranteed to be hard-interruptible by a context.
These are stated boundaries, not guarantees manufactured by piling on workers, caches and locks.

## Verification principles

Evidence for the current input's compilation, tests and real start/stop runs is
[validation.md](validation.md). A wrong stop, a cross-worktree mix-up, ledger corruption or leftover
self-owned processes must be fixed and cannot be offset by performance gains. No claim of overall
speed-up or absence of leaks without repeated measurement on identical input. Long-running use,
attention-quality evaluation and research-grade performance comparisons belong to later iterations and
do not block the first source release behind a week of usage or an arbitrary initial-percentage
threshold.
