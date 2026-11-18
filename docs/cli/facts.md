# Runtime facts

English | [中文](facts.zh.md)

What is true right now: declarations, runs, listeners, health and exit history. Every command on this page only reads; none of them starts or stops a service. The fields an agent branches on are listed in [agent-facing JSON fields](json.md).

## oberth status [path] [--json] [--no-mark] [--only services] [--brief]

Reads a worktree's complete current state: the services the manifest declares, run records, listener
facts, the most recent exit reason and the git summary. When path is omitted the current directory is
used. The command only leaves a "last look" marker in its own state file, and a second run lists the
factual changes in changed; --no-mark reads without moving the marker.

`--only api,worker` keeps only those services and the transitive closure of their `depends_on`;
service/port changes in `ports[]`, `exits[]` and `changed` narrow along with it, while `machine:`,
`runs` and the git summary stay unchanged — `runs` names runs the manifest does not declare, so a
service selection cannot speak for it. Names must already be declared: an unknown name returns `not_found`
and lists the available services, and an empty name is treated as an argument error. Filtering
happens after the full snapshot and the "last look" marker are written, so it never writes a partial
state as the baseline for the next comparison. Code comparison also runs only for the selected
services, and sources outside the scope are not re-read.

`--brief` keeps only runtime facts: the state of services and dependencies, declared/actual ports,
health results, the current exit reason and next step, code changes (`code_changed_since_start`,
`code_changed_files`, `code_hint`), the executable facts (`executable_path`,
`executable_modified_at`, `executable_changed_since_start`), and listening ports plus `observation`.
It does not output `git`,
`drafts`, `changed`, the attention handoff, or the per-service manifest fields; use the full `status`
when you need those.

Per-service facts are present only when they exist: `health` when a health verdict was observed,
`port_actual` while a listener is known, `code_changed_since_start` when the run has a readable start
snapshot. Absent means unknown or not applicable — a down service has no `health` or `port_actual` —
never a negative result; read `running` before those keys.

A running service carries `port_holder` when the listener on its `port_actual` is not that service's
own run: another run's listener, or a process option-berth did not start. It is the same `port`,
`pid`, `command` and `kind` shape as a failure record, with `group`, `name` and `run_id` when another
run owns it. It is a positive fact about the process behind the port; when it is absent the snapshot
did not show another owner, which is not proof that option-berth started the listener.

A service's `last_exit.reason` distinguishes an ordinary crash (`crashed`) from a fixed port taken by
another process (`port_occupied`), and also marks services that failed before the command ever started
(`start_failed`), so "exited after starting", "port conflict" and "failed before starting" do not
collapse into one state.

A recorded run whose process disappears while no runtime service is watching — the host was stopped,
or the run outlived a crash — has its own terminal reason, `ended_unobserved`. The end is known; the
exit status is not, so `code` is `-1` and must not be read as a process status. `at` is when a host
recorded the ending; a read with no host at all leaves it empty because no observation time exists.
The ending is the newest generation under its name, so an older crash from the same name is not
presented as the current outcome. When the runtime service next starts, it records the same ending
durably and removes the mirror entry that carried the evidence.

Under `port_occupied`, `last_exit.port_hint` is the port this run was told to bind, and
`last_exit.port_holder` describes the process still holding that port at read time: `port`, `pid`,
`command`, `kind`, with `group`, `name`, `run_id` additionally for a managed run and `cwd` and
`bind_address` when readable. `kind` is `oberth_run` (a run started by option-berth, possibly from
another worktree), `previous_run` (an earlier generation of the same service), `worktree_listener`
(belonging to some worktree but with no managed run), or `unknown` (no worktree ownership). It checks
only the failing service's own port, not a machine-wide port table; when the holder has already
released it or is not currently observable the fields are absent, and absent means "not seen at read
time", not "the port is free".

Human-readable output is ordered by project, service, dependencies, code and failure reason, with
labels as fixed-width English columns (services · depends on · listeners · code · failed · next ·
drafts · last look) aligned by terminal display width. Each service gets its own line showing both the
declared port and the current runtime facts; the service state word is colored (running green, failed
red, ordinary stop dim), degrading to plain text under NO_COLOR. A portless worker is explicitly
marked no port, and `port: auto` that has not yet been assigned shows auto port unassigned. The
`listeners` line gives every listener that no running service row already names its own home: a port
owned by another run is written with that run's name (`8082 (run finance-web)`), a live run holding no
listener is written with its own name and pid (`worker (run, no port, pid 1234)`) — the pid is
what `kill` accepts, so the line stays actionable — and a listener no
run owns is marked `unowned` with the process and pid when the process table could read them
(`9090 (unowned: node, pid 1234)`). The `resources` line says `N declared running`; runs outside the
manifest are named on the listener line instead, so a worktree with no declared service running does
not read as idle. In the failure section each service
shows only its most recent exit record, and only while that record is still the service's current
`last_exit`: once the service has re-run, or the newest record is a normal exit or a `down` stop, the
service leaves the failure section and the old record stays in the `exits[]` history. Different
services each show one line. A port-conflict failure line writes the holder directly (port, pid,
command and ownership kind), so there is no need to run `lsof`/`ps` again. The
`oberth logs <service> --once` next step is given only when the failure record really left a log
file; that step also appears as `hint` in the JSON `exits[]`, so a human and an agent read the same
guidance. Even if a forced stop left a non-zero signal exit code, services that exited normally or
were stopped do not enter the failure section.

`worktree.services[]` also carries `health_status` (`status`, `code`, `reason`, `observed_at`).
While a running service declares `health:` and the first probe has not produced a verdict yet,
`health_status` is `{status: unknown, reason: pending}` with no `observed_at`; the first verdict
replaces it. A stopped service has no verdict.
`health` is the declared canonical form: an HTTP path, or `tcp:<port>`, or `command:<command line>`;
`prepare_inputs` is the list of files prepare reads, an empty array when not declared. While a start
is in progress, `worktree.services[].starting` gives the instantaneous facts of that start attempt:
`phase` (`waiting_dependency`, `prepare` or `spawning`), `started_at`, the effective `ready_timeout`,
the `elapsed_ms` computed at read time, the `waiting_on` of this wait, and `last_prepare_ms` from the
previous prepare (null when unknown). It is not a run: it does not enter the run records, is not
persisted, does not count as `running`, does not affect `worktree.status`, and disappears when the
runtime service restarts; it is readable during dependency waiting, prepare and spawn, and disappears once the
run is registered. `--brief --json` carries the same `starting` (same fields, copied at read time, not
sharing mutable data with the full document), so brief readers can also tell "starting" from "never
started". Human-readable output shows the service as
`starting · <phase> · for <elapsed> · budget <ready_timeout> · waiting on <dependencies…>`: the
duration is `elapsed_ms` rounded to seconds (under 1 second shows `for <1s`, never `for 0s`), `budget`
uses the published value directly, and the last segment is appended only when `waiting_on` is
non-empty.

Running services carry `stats` (`cpu_percent`, `memory_rss_bytes`, `thread_count`, `uptime`, `state`,
`connections`). A service with a listener samples from the listener row; a portless worker samples
from its running root process, so a worker no longer lacks resource data just because it holds no
port. When there is no sample the fields are absent, meaning **unknown**, not 0.
`worktree.running_services` is the number of declared services that are running; it does not count
runs the manifest does not declare. Read `worktree.ad_hoc_runs` and `worktree.unowned_ports` for the
listeners those runs and processes hold. `worktree.resources` is the worktree aggregate of the
declared services' samples (`cpu_percent`, `memory_rss_bytes`, `services` being the number of
contributing samples). The aggregate appears only when at least one service has a sample and makes
the difference between `services` and `running_services` explicit; with no samples it is absent and
never uses 0 to impersonate a measurement.

Running services also carry `manifest_hash` and `runtime_spec_hash`; when the two differ,
`manifest_runtime_mismatch` is `true`, meaning the current process comes from an old manifest and
should be re-applied with `oberth restart --only <service>`. The hash covers only executable
declarations such as command, cwd, ports, health and dependencies; it does not include environment
variable contents, and does not include `sources:` — changing the scope of the check itself does not
require restarting the process.

If a running service captured its code identity at start, `status` recomputes the content fingerprint
along the paths declared by `sources:` and publishes `code_changed_since_start`: `false` means it was
checked and did not change; for `true`, `code_changed_files` is given when files can be compared
exactly, along with a directly executable `code_hint` (`oberth restart --only <service>`). `code_head`
is the checkout's commit at start, absent when there is no commit or the checkout is not inside Git.
These fields being absent means **unknown** — a run started by an older version, a deleted snapshot,
or source that cannot be read right now — not "unchanged". When the per-file snapshot exceeds the
storage limit, it can still answer whether something changed, but the file count is absent. A client
that already holds the group rows reads the same comparison through the runtime service's `groups.code`
method: the field names and the null-means-unknown rule are the same, and a service absent from the
reply is unknown, not unchanged.

For a run started by this engine, `executable_path` is the resolved program it started,
`executable_modified_at` is that file's modification time read now, and
`executable_changed_since_start` is `true` when the file is newer than the run's start (the published
start time's second precision is absorbed). This is the compiled-artifact counterpart of the source
check: a directly-run binary rebuilt after the run began is reported as changed, while an
interpreter's script is still covered by `sources:`. Absent means **unknown** — the run predates the
fact, the path could not be resolved, or the file is gone — never "unchanged".

When the listener table cannot be trusted (for example an agent sandbox refuses system introspection),
`status` still returns the manifest, services, run records, log paths and recent exits; the top level
carries `observation` (`state`, `code`, `source`, `sources`, `detail`, `hint`, `checked_at`,
`degraded_since`, `recovered_at`). When fully blind, `ports` is
empty and `port_actual` is null; when some lines are unreadable, `ports` contains only the readable
lines, ports that were not read are not in it, and `observation.state` is partial. `up --wait` returns
an observation-class error immediately when a selected service needs listener/health observation and it is not observable, and keeps the run it
just started, instead of waiting out the full timeout and then stopping it; `logs <service>` still
uses the log path registered by the run for managed services and does not depend on the listener
table. Write commands that need to auto-start the runtime service ask the installed user-level service
unit first; without one they run the self-proof probe, and when
blind they refuse to create a runtime service from the current process, suggesting that
`oberth runtime service run --detach` be run in an ordinary terminal.

While system observation is busy, known run/worker state may be published independently. If the last listener batch has expired, `observation.state` is `partial`, code is `observation_incomplete`, and source is `runtime_registry`; missing ports and `machine` availability remain unknown, not free or stopped. A no-port worker without a health check may become ready from its exact run identity alone. Other readiness and forced control paths do not treat this fallback as a fresh listener observation.

The JSON top level always contains:

~~~json
{
  "scope": {"kind": "worktree", "name": "demo", "root": "/work/demo"},
  "worktree": {"name": "demo", "identity": "wt_0123456789ab", "services": []},
  "ports": [],
  "git": {"snapshot": {}},
  "drafts": [],
  "exits": [],
  "changed": {"at": "..."}
}
~~~

The full document's `git.files` holds at most 100 entries; `files_total` is the total before
truncation, and `files_truncated` being true means the list is only a prefix. Use
`oberth git files --json` for the complete file list; it does not truncate.

`--brief --json` is a separate, brief document:

~~~json
{
  "scope": {"kind": "worktree", "name": "demo", "root": "/work/demo"},
  "worktree": {"name": "demo", "identity": "wt_0123456789ab", "branch": "main",
    "running_services": 1,
    "resources": {"cpu_percent": 1.2, "memory_rss_bytes": 104857600, "services": 1},
    "services": [
      {"name": "api", "running": true, "port": 3000, "port_actual": 3001,
       "health": {"status": "ok", "code": 200},
       "code_changed_since_start": true, "code_changed_files": 2,
       "code_hint": "oberth restart --only api",
       "executable_path": "/work/demo/bin/api",
       "executable_modified_at": "2026-10-31T10:00:30Z",
       "executable_changed_since_start": false,
       "stats": {"cpu_percent": 1.2, "memory_rss_bytes": 104857600}},
      {"name": "worker", "running": false, "exit_reason": "crashed", "exit_code": 1,
       "hint": "oberth logs worker --once"},
      {"name": "cache", "running": false,
       "starting": {"phase": "waiting_dependency", "started_at": "2026-10-31T10:00:00Z",
                    "elapsed_ms": 130000, "ready_timeout": "3m0s",
                    "waiting_on": ["db"], "last_prepare_ms": null}}
    ],
    "ad_hoc_runs": [
      {"name": "finance-web", "run_id": "run-7", "group": "demo", "pid": 64530,
       "cmd": "yarn dev", "cwd": "/work/demo/web",
       "started_at": "2026-10-31T09:00:00Z", "ports": [8082]}
    ],
    "unowned_ports": [
      {"port": 9090, "pid": 55120, "command": "python3 -m http.server 9090"}
    ]},
  "ports": [3000, 3001],
  "machine": [{"name": "redis", "port": 6379, "listening": true, "listening_known": true}],
  "exits": [{"run_id": "...", "name": "worker", "reason": "crashed", "code": 1,
             "exited_at": "2026-10-31T09:00:00Z", "hint": "oberth logs worker --once"}]
}
~~~

`worktree.manifest_missing` is present and `true` only when no `oberth.yaml` declares this worktree, so
an empty `services` means "nothing to read" rather than "nothing declared". The full document says the
same thing as a null `config_path`; the brief drops the path, and without the flag a brief reader
cannot tell a checkout waiting for `oberth init` from a configured, idle one. The key is absent when a
manifest exists.

`worktree.ad_hoc_runs` attributes every listener that no running service row already names, plus every
live run this worktree started that holds no listener at all. The run is identified by its run id, not
by its name: an `oberth start` run the manifest does not declare, a stale generation that still uses a
declared service's name, and a declared service's run holding an extra port all appear here, and so
does a portless run — it is a process option-berth owns that `down` stops and `restart --run` replays,
so leaving it out would make it reachable only by scanning the process table. Each row carries `name`,
`run_id`, `group`, `pid`, the recorded `cmd`, `cwd` and `started_at` when the run mirror still has them
(absent means unknown), and the sorted `ports` it holds, which is an empty array for a run holding
none. A run a running service row already names is not repeated here: the service row is its home.
`worktree.unowned_ports` is the rest: listeners with no option-berth run, each carrying `port`,
and `pid` and `command` when the process table supplied them. `ports` stays the flat listener list;
together, a running service's `port_actual`, `ad_hoc_runs` and `unowned_ports` cover every entry in it.
A declared service's listener that another process owns is named by that service's `port_holder`; an
owned run is not repeated there.

`worktree.identity` is exactly the value `${worktree}` expands to in the manifest. It is derived from
the checkout's canonicalized root path, so the same checkout reads the same value every time; when a
human or an agent wants to connect to this worktree's database with `psql`, they read this field
directly to compose the database name instead of computing a hash themselves.

changed is an optional field that is absent on the first run. ports is still the raw listener fact for
scripts to verify against, not an independent product object and not a port list for the whole
machine. runs is every live run this worktree owns, whether or not the manifest declares it and
whether or not it holds a listener — the same set the brief document projects into `ad_hoc_runs`.
A run with no listener appears only here, which is what keeps an `oberth start -d` worker visible
without `ps`. Null means the run set could not be read; an empty array means the worktree has no live
runs, and the two are not the same answer. exits is the newest-to-oldest history of the last 5
finished runs, not the current failure
set; current facts are authoritative in `worktree.services[].last_exit`. `exits[].ports` describes
ports an exited run still holds, which is why exited runs keep an empty array; the failing port is in
`port_hint` and the holder in `port_holder`. Each failure record may carry `hint` (synonymous with
`groups.start`'s per-service hint), the deterministic next step the CLI fills in from whether a log
file exists. Use logs for detailed output. `worktree.services[]`'s `pid`, `run_id` and `started_at`
are provided while the service is being run by option-berth, so even a portless worker can be
verified against the actual run record through these fields; `log_path` is always given by the engine.

A definite abnormal event in the state puts a fact-only handoff in the JSON, whether or not the
optional assessment is enabled. `attn_path` points at a recomputable, expirable agent handoff file
under `BERTH_HOME/attention/`; with no current abnormality the field is absent. **Its presence means
"there is an anomaly", not "the person opted in"** — publishing a local fact sends nothing anywhere,
so consent cannot decide whether it exists. The adjacent `attn` block says whether that handoff can
be assessed now: `enabled` is the consent bit, `reason` is the fixed code when nothing can run, and
`assessment` is `assessed` (the file already carries a validated result), `pending` (an enabled,
complete configuration could assess it), or `unavailable`. An agent may use the fact-only handoff
without a configured model; it must check `attn.enabled` before running `oberth attn assess`.
When there is an abnormality, `attn_revision` is provided as well and must equal the file's
`state_revision`, so the agent can check inside the same `status` response whether the file still
corresponds to current facts. An
undeclared listener is not an abnormality: `status` already lists the port with its owner, and a
process the agent did not start is not one it should stop. The core file is
`oberth.attn/v1`, containing `event_id`, `state_revision`, `worktree_root`, optional `branch`,
`generated_at`, `fresh_until`, `first_seen`, `last_seen`, optional `recovered_at`, `event`, an
optional full `events` array, `evidence` and fixed `options`, and containing no shell commands or
model invocation results; `event` is kept as a compatibility field for the first abnormality, and when
several are abnormal at once `events` is authoritative; an optional attention adapter may add
`assessment` to the file, but the agent must still re-read `status --json` first and verify the
worktree, `attn_revision` against `state_revision`, and `fresh_until`, before offering the user a
choice. `assessment.context_id` (if present) is only the adapter's hash cache key over the current task
and the authorized-option allow-list; it is not a runtime fact and grants no execution permission.
This file is a derived notification, not a new source of runtime truth. The model client is not on the runtime service's or
`status`'s control path: the deterministic part lives in `internal/attn`, the model client in
`internal/attn/model`, and `internal/cmd/attention_boundary_test.go` checks that dependency boundary.

## oberth logs <service|port> [--once] [--lines N] [--follow] [--ip address] [--json] [--run current|<id>] [--first-error]

Views logs by the service name in the manifest; the port form continues to work. A name the manifest
does not declare also resolves when a recorded run carries it — an `oberth start -d --name X` run, the
same name `status` publishes in `ad_hoc_runs` and in the exit hint `oberth logs X --once` — and the
read then uses that run's log file. A declared service name wins over a recorded run of the same name.
When the service has a listening port, the command reads that process's logs; a portless worker reads
that run's service log file. By default it prints the current run's most recent lines first and keeps
following while that run is still writing; --once reads once and exits. The read is scoped to the
current generation
whenever its start offset was recorded — online or with no runtime service at all — so a file
appended to across runs does not mix the previous day's lines into this run. When no current
generation has a recorded start, it reads the whole file and marks the document
`run_start_unknown: true`, and the human output says the scope is unknown. --ip disambiguates when
the same port is bound on several addresses. --json --once outputs one document:

~~~json
{"source":"/path/to/service.log","lines":["..."],"truncated":false,"service":"api","run_id":"...","pid":1234,"status":"running"}
~~~

Printed lines follow the color rule: when color is off — `--no-color`, `NO_COLOR`, `color: false`, or
a stdout that is not a terminal — the service's own SGR color sequences are removed from the printed
`lines` and `first_error.text`, while the log file keeps the bytes the service wrote. With color on,
lines are printed as written; other escape sequences (cursor movement, screen control) are never
rewritten.

`--run current --first-error`'s document:

~~~json
{"source":"/path/to/service.log","lines":[],"truncated":false,"service":"api",
 "run_id":"...","log_offset":1234,"first_error":{"line":42,"text":"ERROR could not connect"}}
~~~

Service log files are appended across runs. `--run` reads only one run's own part of that file: from
`log_offset` to where the next run of that service started writing, so a later generation's lines —
including the error it died on — are not reported as this run's. A run that is still writing owns
everything after its start; two runs that overlap in time write interleaved bytes, and their parts
cannot be told apart. A run that has ended owns only the bytes already in the file: when no later
generation has recorded where it took over — the newest finished run — its part ends at the end of
the file as it is when read, so following it prints and returns instead of waiting for a generation
that has not started yet. `--run current` selects the service's current run, and any other value is an
exact historical `run_id` from `status --json`. `current` prefers a process that is still running and
only falls back to the most recently finished one when there is none. Following a run that has ended
prints its lines and stops: nothing arrives for it afterwards. `--run` accepts only service names and
cannot be combined with `--ip`. A service's part of its log file is recorded on disk — the mirror for
a live run and the exit row for a finished one — so the form works without the runtime service for a
service log file; only listener output needs a host, and with none reachable that case returns
`runtime_service_unavailable`. Old run records without an offset return not_found with a suggestion to restart
the service once. When the file has been rotated and the recorded part is no longer in it, reading
falls back to the beginning of the file, and the JSON uses `run_start_unknown: true` to say that the
lines this time may include other runs.

`--first-error` is used together with `--run`: it locates the first matching line of that run from a
fixed vocabulary (case-insensitive error, exception, fatal, panic, traceback) and returns the line
number and the original text. It is a position, not a root-cause judgment; with no match the JSON
carries `no_first_error: true`. `--first-error` implies `--once`.

The `--run` JSON document adds `log_offset`, an optional `run_start_unknown`, and `first_error`
(`line`, `text`) or `no_first_error` on top of the original fields. Line numbers are counted from the
start of that run; when reading falls back to the beginning of the file, line numbers are counted
against the fallen-back content too, and `run_start_unknown` is the precondition for reading those
line numbers.

Follow mode uses NDJSON, each line carrying `source`, `line` and the available service and run
metadata; a one-shot read also gives `exit_code` and `reason` when the service has exited. A port that
cannot be found returns not_found; several bindings without --ip return ambiguous.

## oberth events

For agents to subscribe to this machine's runtime service state changes. During parallel integration work an agent should actively establish and consume subscriptions according to the relationships explicitly given in the task; a human running this command is mainly troubleshooting. `--worktree PATH_OR_ID` includes the same repository's
other worktrees along existing Repo relationships; `--repository REPO_ID` is a repeatable additive filter, and `--workspace NAME` joins an explicit temporary integration relationship;
the first NDJSON line is directly `{"type":"state.snapshot",...}` and each following line is directly `{"type":"state.changed",...}`.
The first time a workspace establishes a live subscription it must supply a worktree or repository as well; later subscribers may carry only the workspace;
`--once` reads the snapshot only and registers no workspace membership.
The RPC's filtered `state.subscribe` uses `filter: {worktrees, repositories, workspace}` and must set
`events` to `true`; use `state.snapshot` to take a single snapshot.
Snapshots and changes contain only the runtime fact summaries of worktrees, services and ports, not commands, working directories, logs or environment variables;
when details are needed, go back to the relevant worktree and run `oberth status --json`. A client should keep the `seq` from a snapshot or change, and
on reconnect after a drop pass the last applied sequence number as `after_seq` to `state.subscribe`; when the runtime service can cover it, it first returns a
baseline snapshot for that sequence and then replays increments in order, and when the window has expired or the state chain is discontinuous it returns the current full snapshot. `oberth events` reconnects automatically with the last `seq` after a transport drop or queue overflow;
workspace membership changes still go by the new snapshot, and an expired queue window also falls back to a full snapshot;
membership changes notify existing subscribers with `changed: ["workspace_changed"]`. If you connect to an old runtime service, run
`oberth runtime service restart` first to obtain the `state.scope` capability.

## oberth worktrees [--gone] [--json]

Lists, read-only, the `${worktree}` identities this machine's runtime service has seen, the checkout root path
each was derived from, the most recently published group name, and whether that path still exists now.
It is an identity ledger, not runtime fact: rows remain after a checkout is deleted or moved, `exists`
is re-determined against the filesystem on every read, so an identity exists again when the same path
reappears; `last_seen` is only the time of the most recent ledger write (runtime service start or group-name
change), not a heartbeat from every scan.

`--gone` keeps only identities whose checkout no longer exists, so a human or an agent can find the
databases and data directories left behind under an old identity (`app_${worktree}`,
`.data/${worktree}`). Deletion is still done by a human: the command does not delete ledger rows,
databases, data directories or running processes; to stop a service that is still running use
`oberth down --identity <wt_…>`.

When a runtime service answers, each row also carries `running_services`: the number of declared services
running under that identity. It is a single read-time connection to the group facts and is not written
back to the ledger. Without a runtime service the field is absent — the ledger does not store run state, and
absent means **not observed**, not 0.

~~~json
{"worktrees":[
  {"identity":"wt_0123456789ab","root":"/work/demo","group":"demo","exists":true,"last_seen":"2026-10-30T09:00:00Z","running_services":2},
  {"identity":"wt_abcdef012345","root":"/work/old","group":"demo@old","exists":false,"last_seen":"2026-10-28T18:00:00Z"}
]}
~~~

With no runtime service, or no ledger yet, it returns an empty list; it does not start a runtime service or create a
database just to read this fact. The runtime service is the ledger's only writer: it records identities when it
scans manifests and backfills by recorded root path after a restart; read commands such as `status`
and `down` do not write the ledger.
