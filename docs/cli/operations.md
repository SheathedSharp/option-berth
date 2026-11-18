# Operations

English | [中文](operations.zh.md)

Make services run or stop. Each command below is its full contract, including what it refuses to do and what it keeps running.

## oberth up [project] [--only name,...] [--no-deps] [--wait|--ready] [--wait-timeout duration]
[--allow-outside-home] [--json]

Starts services in the order of `oberth.yaml`'s depends_on. When `--only` names services, their
transitive `depends_on` closure is started along with them by default (for example `--only web` with
`web → api → db` starts db, api, web in that order); explicitly named services keep their named
identity, and services pulled in as dependencies are marked in the output with the named service that
pulled them in. `--no-deps` starts only the named services themselves and does not expand the
dependency closure; without `--only` it changes nothing (everything is started anyway). In
human-readable output the pulled-in lines carry a dim note (such as `started (dependency of web)`,
`skipped · required by web`), and the summary adds a separate line
`included dependencies: api, db` when it is non-empty. In `--json` they remain ordinary `services[]`
entries carrying `included_by` (the sorted named root services); the summary carries `included`
(sorted). Capability negotiation: an old runtime service does not know `include_dependencies`, and the new CLI
sends it only when `--only` names at least one service, in which case an old runtime service produces a direct
error suggesting a runtime service restart or `--no-deps`; without `--only` (or with `--no-deps`) it is not
sent, so ordinary `up` is unaffected. When a service has `prepare:`, that prerequisite command is run
directly in the same cwd first, and only on success is the long-running `cmd:` started; the
prerequisite's output goes into that service's log, and on failure the service is not started. When a
service has `env_file:`, both prepare and the long-running command read the variables from the file
first (see "Shared environment files"). `prepare_inputs:` declares the input files that command reads
(relative to the service cwd, for example `[package.json, package-lock.json]`); when the previous
prepare succeeded and those files' contents are unchanged, this `up` skips prepare outright and does
not reinstall dependencies. The fingerprint is recorded only after the previous success, and a
missing or unreadable input always forces a rerun; the cache lives in `$BERTH_HOME/prepare`, and
deleting it only makes the next start slower. `sources:` declares the files or directories used to
decide "is the process running the current code" (relative to the service cwd, that directory by
default); at start the content fingerprints of those paths and the HEAD at that time are recorded, and
`status` recomputes them and publishes `code_changed_since_start`. In a Git checkout only tracked
files and untracked files that are not ignored are included; `node_modules`, `target`, `dist` and the
like do not enter the fingerprint — even when they are explicitly written into `sources:` — because
they are usually artifacts produced from the source; non-Git directories only apply a fixed ignore
list. The `sources:` paths themselves must not pass through symbolic links (declare the link target
instead); deeper links inside a directory are recorded as the link itself and not followed to the
target. Multiple services with the same default source tree (for example all defaulting to the same
repository root) report changes together; to distinguish them, write the `sources:` each one actually
reads. Explicitly writing `sources: []` turns this check off, which suits services that hot-reload
their own code. Dependencies with a port wait for a listener and portless workers wait for their run
record to become active; services already running are skipped; each service has its own log file and
run record. A listener option-berth did not start is still a skip, but its reason says so and its
`hint` names the port to free; option-berth does not take over a process it did not start. With no
arguments the nearest manifest from the current directory is used, and a project
name may be given explicitly. `ready_timeout:` declares a service's readiness budget (a Go duration
string such as `90s`, default 30s, must be positive): successors in `depends_on` use it when waiting
for that service, and `up --wait` uses it too. `prepare:` does not count against this budget. When the
manifest declares `ready_timeout:`, the CLI requires the runtime service to announce the
`groups.start.ready_timeout` capability; an old runtime service gets a suggestion to run
`oberth runtime service restart` instead of silently waiting 30s. When starting by project name
(`oberth up <project>`, `oberth restart <project>`) the CLI has no manifest in hand, so this check
instead finds the project's manifest path through the runtime service's `state.snapshot` and parses it locally,
then applies the same capability rules; when the manifest cannot be found or read, the start is not
blocked and the runtime service reports its own error.

`health:` has three forms: an HTTP path (`/healthz`, the default form), a TCP connection
(`health: {tcp: 5432}`, whose port must match the service's declared port) and a command
(`health: {command: pg_isready -h 127.0.0.1 -p ${port}}`). The command runs in the service cwd, is
split into argv by the `cmd:` rules (not through a shell), supports `${port}`, `${url}`,
`${<service>.port}` and `${worktree}` references, and exit code 0 counts as healthy. HTTP and TCP only
say the port is reachable; services such as databases, whose port opens before the protocol is ready,
use a command check. Health results and dependency waiting use the same fact.

On success, human-readable output reports per service and then gives a summary; during `--wait` a
single dim hint line is printed first and results follow line by line. --json outputs one value
containing services and the summary fields. Each `services[]` has a structured `state`: `started`,
`skipped` or `failed`, and on failure also a machine-branchable `reason` and a directly executable
`hint`, with the original `error` kept for humans. `hint` only points at evidence that really exists:
`oberth logs <service> --once` only if the process actually came up, the **dependency's** logs when a
dependency was not ready, the rejection's own remedy when the process was refused before starting (for
example the worktree is outside home), and nothing when there is no executable next step. When the
failure reason is `port_occupied`, `services[]` also carries a `port_holder` with the same shape as in
status (`port`, `pid`, `command`, `kind`, plus `group`/`name`/`run_id` when managed), so there is no
need to look up the process table yourself. `services[]`'s `ready` is the CLI's annotation, not a
runtime service fact: `true` after `--wait` confirms readiness; when not waiting by default, `started` services
carry `false` and the human-readable output shows `~` and `started, readiness not confirmed`;
`skipped`/`failed` do not carry the field. `false` is not a failure, and `state` still describes the
result of the start action. When an unconfirmed `started` service remains, the command also publishes
a next step: `next` in the JSON document and a `next:` line in the human output, both naming
`oberth up --only <services> --wait --json`. Wait determination goes by run identity first: this start chunk's
`run_id`, the running run a skipped chunk points at, or the same run observed for the first time. Only
old records with no run_id at all fall back to exit time, with the wait start aligned to the second,
so an exit recorded within the same second still counts as this round. Old exit records from other
runs are not read by `up --wait` as this round's start failure.

`--wait` (an alias of `--ready`) waits, before returning, for the services selected this time
(including services already running and not restarted) to actually listen; services that declared
`health:` must also pass the corresponding health check (a command check does not require the service
itself to listen). A portless worker is ready when its run record is active. There is no waiting by
default; without `--wait-timeout` each service waits by its own `ready_timeout:` (default 30s): a
service newly started this time counts from that run's start time in the state row, while an already
running skipped service, or one whose start time cannot be obtained, counts from the start of the
wait. When `--wait-timeout` is given explicitly it is the overall ceiling for the whole wait and only
tightens, never loosens, each service's `ready_timeout:`: each service's deadline is whichever of the
two comes first; it must be positive and must enable `--wait` or `--ready` at the same time, otherwise
it exits 2 without running the start; a service that times out has state `failed` with reason
`ready_timeout`, the message says whether the timeout came from that service's readiness budget or
from the caller's `--wait-timeout` ceiling and names the one that raises it (`ready_timeout:` for the
budget, `--wait-timeout` for the ceiling), and the command exits with code 1. Only runs newly started
this time that time out enter stop cleanup; existing runs are not stopped or restarted because this
wait failed. The cleanup names each run it started by `run_id`: a run another controller started for
the same service while the wait was still running is a different run and is left alone. A runtime service that
does not announce `groups.kill.expect_runs` cannot scope a stop to named runs, so it is not sent a
name-only cleanup: the command fails and says the timed-out runs are still running. Readiness is
attributed by the same identity: when the service row's live run is not the run this wait is about, the
wait ends immediately with state `failed` and reason `run_replaced`, because the replacement's listener
and health are its own evidence, not the original run's; nothing is stopped then, since the replacement
is not this invocation's run. A row that names no run cannot confirm a named one: a listener not
attributed to the run keeps the wait going instead of counting as its readiness. A chunk with no
`run_id` at all carries no identity to compare, so the service row is read as before. The `started` /
`skipped` summary describes the start action, and readiness failures appear separately in `errors`
and in each service's `state`. For runs newly started this time that
were cleaned up, the engine writes the same `ready_timeout` into the service's last exit facts, so
`status`, the desktop service row, attention and the event stream do not show it as an ordinary manual
stop. Health facts carry `status`, `code`, `reason` and `observed_at`; when a dependency declared
`health:`, successors wait for that dependency's health status to be `ok`. The dependency wait budget
is the maximum of the depended-upon services' own `ready_timeout:` values in `depends_on` (undeclared
dependencies count as 30s), not the waiter's own; when the dependency wait itself fails, the service's
last exit reason is `dependency_timeout` or `dependency_not_ready` respectively, and like a start
failure it is kept in the status, attention and state changes. If a dependency was already attempted
by this `groups.start` and has already exited during the wait, the successor no longer waits out the
budget: it fails immediately with `dependency_not_ready`, the message carries the dependency's exit
code and reason, and `hint` points at the dependency's logs. When the dependency is still alive but
not yet listening, it still waits out the budget as `dependency_timeout`. Any service start failure
returns exit code 1, and the failed service keeps its exit reason for status to show. By default
services may only be started inside the user's home directory; only explicitly using
`--allow-outside-home` lifts this restriction.

When the runtime service announces the `groups.start.phases` capability, `up` sends `progress: true` in the
request and receives progress chunks with `state: "starting"` as services enter the
`waiting_dependency`, `prepare` and `spawning` phases: `phase`, `started_at`, `ready_timeout`,
`elapsed_ms`, `waiting_on`, `last_prepare_ms`. `waiting_on` shrinks as dependencies are polled: each
time a declared dependency is observed ready it disappears from the set, and the set is empty when all
are ready; when observation itself fails the last known set is kept, so "cannot read" is not shown as
"not waiting". These chunks are diagnostics, not results: they do not enter `services[]`, do not take
part in readiness waiting, and do not affect the summary. Human-readable mode prints them as dim
progress lines; with `--json` the progress lines go to stderr only, and stdout still holds exactly one
final JSON value with no diagnostics mixed in. An old runtime service does not know `progress`, and an old CLI
does not send it, so behavior is as before.

For example, a partial success is still one JSON value:

~~~json
{"services":[
  {"service":"api","state":"started","ready":false,"pid":1234,"port":3000},
  {"service":"worker","state":"failed","reason":"start_failed",
   "error":"exec: \"nosuchcmd\": executable file not found in $PATH",
   "hint":"check the command and its working directory"}
],"started":["api"],"skipped":[],"errors":["worker"]}
~~~

## oberth restart [project] [--only name,...] [--run id|name] [--force] [--allow-outside-home] [--json]

Stops the project's services first and then starts them again from the manifest. Human-readable output
reports the stop result first, then an empty line, then the start result. `--only` restarts only the
named services while dependencies and other services keep running; this kind of restart preserves the
worktree's `port: auto` reservation so the service gets its original address back. `--force` skips the
graceful stop. --json returns one value in which `stopped` is the stop result and `services` plus the
summary fields are the restart result. `restart` does not wait for readiness, so `started` services
carry `ready:false`, and the human-readable output likewise marks readiness as unconfirmed.

If cancellation or a transport failure interrupts the sequence after a stop receipt is known, that receipt and any received service outcomes are still reported. JSON keeps the same `stopped`/`services`/`start_id` shape; without a terminal start reply, `started`, `skipped` and `errors` are null (unknown), not a successful empty summary. The operation error retains the standard stderr error shape and nonzero exit status (130 for interruption). Human output likewise keeps known outcomes without printing a terminal start summary. A failed stop never starts services; failure before any stop receipt does not invent one.

A run started with `oberth start` has no manifest entry: `--run <id|name>` restarts it from its
recorded argv. It stops that exact run through `groups.kill`'s run-id selector (a portless run is
included, and the ending is recorded as `stopped`), then starts the recorded argv, cwd, group, name
and port hint again through the host. The argv is the exact vector the run was started with, recorded
from this version on; a run started before that cannot be replayed faithfully and returns `not_found`
with its recorded start line instead of guessing at quotes. A name is accepted only when exactly one
live run in the group has it; an id is always unambiguous. `--run` and `--only` select different
subjects and cannot be combined. `--json` returns `restarted_of` (the original run's id), `stopped`
(absent when the run had already ended), and the new `run_id`, `pid` and `log_path`. The exact-run
stop requires a host that advertises `groups.kill.runs`; an older host is refused rather than
widening the stop to the whole service or group.

Without `--run`, a run the manifest does not declare cannot be restarted. When `restart --only`
names such a run and the run ledger knows it, the `not_found` hint prints the recorded start line
(cwd, group, name, port and command) instead of pointing back at `status`. That hint does not depend
on the manifest being there: a worktree with no `oberth.yaml` at all still gets the recorded start
line, because the run ledger rather than the manifest is what knows it. A name the ledger does not
know falls back to the missing-manifest `not_found` and its `oberth init` hint.

A listener option-berth did not start is reported by the start half the same way `up` reports it:
skipped, with a reason and hint naming the process and port. `--force` only skips the graceful stop;
it does not take over a process option-berth did not start.

Before the stop, `restart` reads the same code comparison `status` publishes (through the narrow
`groups.code` call) for the services it is about to stop, and prints a warning before stopping when a
running service's `sources:` changed since it started, or the executable that run started was rebuilt
after it. The warning never blocks the restart and never invents a build command: it says the restart
will keep running the old build, or that it will start the new binary. `--json` carries the same facts
as `warnings` (`service`, `code_changed`, `code_changed_files`, `executable_changed`); the field is
absent when there is nothing to warn about. A comparison that did not run (a run predating the fact,
a deleted snapshot, an older runtime service without `groups.code`) produces no warning — unknown is
not "unchanged".

## oberth down [project] [--worktree path | --identity wt_…] [--force] [--json]

Stops all services in the project's manifest and, once the action confirms they can be released,
returns the `port: auto` reservations. With no arguments the current directory's manifest is used.
`--force` uses a forced signal directly and does not skip the run-identity or reservation-release
gates. On success `--json` returns the stop result and the number actually released. A service or run
that could not be stopped is not success: `--json` still returns the envelope — `ok` false, with the
failed rows carrying the reason — and the command exits 1, so a caller that reads only the status
does not go on as if the project were down. `machine:`
dependencies declared in the project manifest are read-only as to their listener state; `down` does
not stop them. `down` stops every run in the group, including ad-hoc runs started by `oberth start`
that the manifest does not declare. `up` starts only declared services, so an ad-hoc run removed by
`down` has to be started again with `oberth start`; `worktree.ad_hoc_runs` in the brief document
names such runs while they are alive.

`--worktree PATH` stops by checkout root path, `--identity wt_…` by `${worktree}` identity. Neither can
be combined with a project name or a manifest path. They are the explicit entry point after a worktree
has been deleted or moved: while the directory still exists the runtime service re-reads that manifest; once it
is gone, the runtime service stops the listeners and runs still belonging to it under the group name recorded
in the identity ledger, and returns the `port: auto` reservations recorded for that worktree. An
identity that cannot be found returns not_found and does not guess a group. When the target already
has no runs or reservations, an explicit identity is still a successful empty stop. `--identity`'s
value comes from `status --json`'s `worktree.identity` or from `oberth worktrees`.

Stopping by project name is group-name scoped: different worktrees of the same repository are
published as `<project>` and `<project>@<worktree>`, and different names do not affect each other.
When two checkouts really do take the same group name (for example different repository directories
with the same name, or two same-named linked worktrees under different parent directories), the
runtime service's groups are already merged and any group-scoped stop hits both; in that case `down <project>`
and an explicit identity both return ambiguous, suggesting `kill` by port or pid instead of picking
one for the caller.

On cancellation, an incomplete stop, an observation failure after the action, runs or listeners still
registered, or a reservation that has already been refreshed, no unconfirmed reservation is returned;
the existing RPC error is returned with a suggestion to check `status` and the logs and then retry.
Signals already sent are not undone by this error; `not_found` alone does not prove the port can be
released. A real stop of a portless run needs an accurate birth identity. Low-level dry-run and scoped
stops do not release reservations; `restart --only` therefore keeps the original allocation. This
promises no atomicity between OS observation and ledger writes, nor that arbitrary old versions
writing the same ledger at the same time will work.

## oberth start [flags] -- <command> [args...]

`start` runs only one low-level entry point, `oberth start [flags] -- <command> [args...]`; day-to-day
project services use `up` / `down`. `${<name>.port}`, `${<name>.url}` and `${worktree}` in command
arguments are expanded before execution from the current worktree's address book: services in this
manifest, `machine:` references and `workspace:` references all work, and `port: auto` takes the
current actual port. So `oberth start -- curl '${api.url}/health'` reaches this checkout's service and
does not connect to the other one during two-worktree integration work. A declared reference that
currently has no address makes the command fail outright rather than executing `${...}` literally;
bare `${port}` / `${url}` expand only when `--port` is given. Other arguments are passed through
unchanged. A detached start (`-d`) also publishes the listener the runtime service attributes to the run
(`port N`); with `--port` it waits briefly for that attribution and says when no port has been
observed yet, because the command may bind a fallback port instead of the hint.

## oberth kill <port|pid|run>...

Stops listeners and runs by name; --all is an explicit batch operation and still confirms.
A positional argument is read as a port, then as a pid, and otherwise as a run name or run id — the name `status`
publishes in `worktree.ad_hoc_runs`. A name is resolved against the run ledger with the same rule `restart --run`
uses (an exact run id always wins; a name must identify one live run), and it is scoped to the worktree the command
runs in. A named run is stopped as a whole tree — the processes under its root are the run — including one that holds
no listener, and a name whose generation already ended is nothing to stop rather than a target. Where a runtime service
answers, the stop is the same exact-run stop `restart --run` uses, so the host pins the generation from the ledger that
owns it; without one, the CLI pins the recorded root pid and start time itself and refuses a run whose start was never
recorded. `--no-escalate` cannot be combined with a run name: a run is always stopped whole. The two vocabularies are
not mixed in one request: give either run names or ports/pids.
Human-readable result lines use the reader's vocabulary: `stopped (SIGTERM)` / `stopped (SIGKILL)` / `stopped container`,
never printing the signal name directly; when `down` stops a portless run it shows the service name (the name in the manifest), not the interpreter name from the process table.
A released address line reads `released <url>`. `--json`'s `method` field is still the wire enum
(sigterm/sigkill/docker_stop/none); human and machine readers each use their own vocabulary.
The old group/session selectors are kept only for migration and do not appear in public help.
