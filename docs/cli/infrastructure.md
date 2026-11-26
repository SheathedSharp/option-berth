# Infrastructure and display

English | [中文](infrastructure.zh.md)

The Runtime Service, this installation, the self-checks, the optional improvement report, and what the first screen shows.

## Infrastructure commands

- oberth doctor: checks the manifest, runtime service, CLI path, log directory and whether the current process can see listeners, and gives actionable repair hints.
  `--only listener_observation` binds a real local port and then uses the same collector as `status` to confirm it can be seen:
  it reports observable / blind / bind_denied; in CLI mode it also carries the runtime service's self-proof result, and a blind runtime service also fails.
  `--only runtime_service_build_matches` compares the CLI's and the runtime service's build commits; the same version number does not mean the same build.
  A missing commit identity or a failed status RPC reports warn, does not mark a match, and does not restart the runtime service automatically.
  `--only agent_skill` reads the skill already installed under `HOME` (`~/.claude`, `~/.codex`, and `~/.config/opencode` skill directories) and compares each copy's `SKILL.md` `metadata.version`: nothing installed is `skip`, a single copy is always `ok`, and only disagreeing versions or a symlink pointing at a checkout that no longer exists report `warn`. It does not go online, so it does not know which version is current and never calls one copy outdated; it does not install, update, or repair the skill.
  `--only agent_integration` reads `AGENTS.md` and `CLAUDE.md` in the directory that holds the nearest manifest and reports `ok` when either mentions oberth; no manifest is `skip`, and a manifest whose instruction files do not name oberth is `warn`, with the paste section in [Integrating orchestration tools](../integrations.md) as the fix. It reads the files and never edits them; it does not know whether a tool actually loads them.
  `--only project_config` still warns when a git worktree has no `oberth.yaml`, and the fix remains `oberth init`. When the directory is not inside a git worktree (for example the home directory), the same row warns that this is not a project and tells you to run doctor inside a project or pass `--project`; it does not recommend `oberth init` there.
- oberth runtime service run|status|path|stop|restart|log|schema: manages or inspects the background collector. `runtime service status --json`
  carries `observation` (`state`, `code`, `detail`, `hint`, `checked_at`), saying whether the runtime service itself can see listeners;
  `runtime.service.hello` carries the same result, and clients must not take a blind runtime service's empty port table as fact.
  It also carries `scanner`: `scanning` when there is at least one subscriber, or `parked` when there are none. A parked loop does not keep scanning at `scan_interval_ms`, so an old `last_scan_at` is expected until a state read or subscription wakes it.
  The same host answers `runtime.world.read` under the `runtime.world` capability and returns the reduced Runtime World (`oberth.runtime.world/v1`) documented in [runtime facts](facts.md#oberth-runtime-inspect-path---json), including its `coverage`, `health` and `diagnosis`.
  `run` is foreground unless `--detach` is given; `--persistent` disables idle shutdown regardless of `runtime_service.idle_timeout`, and it is what the user-level service unit passes. Where that unit is installed, `restart` restarts the unit and never starts a detached process of its own.
- `oberth -v` / `oberth --version`: prints the current CLI version concisely; `oberth config init|path|edit`, `oberth runtime service run`, `oberth version`, `oberth completion`: configuration and runtime environment entry points.
- `oberth version --json` outputs `version`, `commit`, `built` and `platform` for the current build, for install check scripts to confirm the build actually running.
- `oberth version --check` reads the latest release tag and reports whether this machine is behind; `--check --json` adds
  `latest` on top of the original fields. It only reads release information: it does not download, does not install, and does not replace the running binary. Terminal commands reuse the same
  check result: the first screen shows the local version with a full upgrade block, and any other command that writes human output ends with a two-line upgrade footer when this build is behind.
  A release is announced once per build. A successful result is cached for 5 minutes. The first screen may wait up to 2 seconds for the answer, because it is
  where the version was asked for; every other command only reads an answer that is ready or nearly ready, waiting at most 250 milliseconds; an explicit
  `--check` may wait up to 6 seconds. A failure is remembered with its kind: a check this budget cut off is retried after 2 minutes, while an unreachable
  release feed or an answer that is not a release is remembered for 30 minutes. `BERTH_NO_UPDATE_CHECK=1` turns the automatic notices off. JSON output,
  `--help` and an explicit `--check` never add the footer.
  The check reads GitHub's release metadata and a CDN copy of the tag list, so a slow or blocked GitHub does not hide a release.
  When no source answers, `--check` exits non-zero and explains why, and no terminal path turns an unknown answer into "latest": the first screen says the
  update check is unavailable and names `oberth version --check`, while the footer stays silent. The check result is cached in `BERTH_HOME/update-check.json`
  (default `~/.option-berth/update-check.json`), reused only for these notices and not a runtime fact.

`oberth version --json`'s stdout is one JSON value:

~~~json
{"version":"v0.1.0","commit":"<git-sha>","built":"<UTC timestamp>","platform":"darwin/arm64"}
~~~

`--check --json` adds `latest` to this object:

~~~json
{"version":"v0.1.0","commit":"<git-sha>","built":"<UTC timestamp>","platform":"darwin/arm64",
 "latest":{"version":"v0.1.1","checked_at":"<UTC timestamp>","update_available":true}}
~~~

`latest.version` is the latest tag reported by the release source; `update_available` is `true` only
when the local version is strictly older, and `false` both when the local version is newer (for example
a build from `main`) and when they cannot be compared (for example `dev`).

### Runtime Service protocol 3 switch

The Runtime domain remains the worktree → manifest → service → run → runtime-facts chain. Runtime Service is its background host. Product 3.0.0 / protocol 3.0.0 removes the old public host namespace rather than keeping aliases. Capability negotiation is only between matching protocol majors; it does not make a v2 host acceptable.

Protocol `3.1.0` adds one capability, `runtime.world`, and its `runtime.world.read` method. The addition is compatible with 3.0.0: it renames nothing and removes nothing, and a client that does not ask for it reads the earlier surface unchanged.

<!-- retired-runtime-v2-begin: refused names and manual switching only -->
| Retired v2 input | Protocol 3 replacement |
|---|---|
| `oberth daemon …` | `oberth runtime service …` |
| `oberth serve [--detach]` | `oberth runtime service run [--detach]` |
| `--no-daemon` | `--no-runtime-service` |
| `daemon.*` RPC methods | `runtime.service.*` RPC methods |
| `daemon_*` JSON fields/check IDs; `Daemon*` wire definitions | `runtime_service_*`; `RuntimeService*` |
| `daemon:` config stanza | `runtime_service:` |
| `daemon.sock`, `daemon.log`, endpoint `daemon.lock` | `runtime-service.sock`, `runtime-service.log`, endpoint `runtime-service.lock` |
| `BERTH_SOCKET` | `BERTH_RUNTIME_SERVICE_SOCKET` |
| `BERTH_DAEMON_DETACHED` | `BERTH_RUNTIME_SERVICE_DETACHED` |
| `BERTH_NO_AUTOSTART` | `BERTH_RUNTIME_SERVICE_NO_AUTOSTART` |
| `BERTH_ALLOW_TEST_DAEMON` | `BERTH_RUNTIME_SERVICE_ALLOW_TEST_BINARY` |

The new CLI rejects retired commands, flags, config keys and non-empty retired environment variables with a migration error, without printing environment values. Malformed YAML and invalid/relative `previous_service_lock_dirs` are refused before operations, never replaced by default safety guards; configuration reads and writes use the same validation. Retired `daemon.*` requests receive `not_found` with a protocol-3 hint. There is no old socket probe, command alias, RPC dual route, automatic old-host shutdown or data deletion.

Before a real switch, stop the old host explicitly with its own matching v2 CLI (`oberth daemon stop`, using the old endpoint setting if it was custom), and verify that its status is stopped. Then rename shell/config keys, retain the database and run ledger, and start the matching new CLI with `oberth runtime service restart` (or `oberth runtime service run --detach` when no user-level unit is installed). Do not use the new `restart` or `doctor --fix` to stop a v2 host. This source change does not perform that switch or install anything.

A `daemon.lock` file can remain only as an additional inter-generation interlock, never a transport fallback. Optional `runtime_service.previous_service_lock_dirs` adds held guards for known old endpoints; database safety does not depend on enumerating those directories. The new host takes SQLite EXCLUSIVE/WAL ownership before listening, restoration or publication, on one non-expiring connection. An unmodified v2 Store already holding the database prevents admission; while the new owner runs, old/new writers cannot reopen it, even through another endpoint, `BERTH_HOME` or file alias. A quiescent old process with no open SQL connection is not claimed to have been identified or stopped: it cannot resume database writes while the new owner holds SQLite, and the explicit old-host shutdown step still applies. Each write commits normally; no transaction is held until shutdown. Lost owner connections fail closed instead of silently reconnecting. All producers stop before storage closes, and the endpoint lock is released last.
<!-- retired-runtime-v2-end -->

`runtime.storage.read` reads the existing owner Store with `section` equal to `schema`, `worktrees` or `run_exits`. The reply repeats `section` and includes that section's receipt (`schema` object, `worktrees` array or `run_exits` array); an observed empty collection is `[]`, not a missing receipt. Schema includes path, version and file size. Worktree rows and the at-most-100 durable exits use the generated schema's snake_case fields. No SQL text or write is accepted. Local CLI reads send an absolute `expected_db_path`; the owner checks file identity, rejecting a different or missing database at the reached endpoint while accepting aliases of the same file. Other RPC callers may omit this local-path assertion when deliberately reading the selected endpoint. CLI history, worktree identities and doctor use this path online; only an absent owner permits the same database to be opened read-only offline. A reached owner's failure, malformed receipt, cancellation or ownership failure is not an empty success. Reads waiting for the owner connection honor cancellation.

Offline read-only access does not migrate or modify database/WAL contents, but SQLite may create/update its `-shm` coordination sidecar when reading a WAL left after a crash; it is not a zero-filesystem-write mode.

The owner serializes SQL reads and writes on its single connection; it does not retain the old pooled-reader concurrency guarantee. External SQLite readers and live SQLite backup connections are refused while ownership is held. Stop the matching Runtime Service normally before taking an offline database backup; copying only a live main database file is not a supported backup.

Runtime Service ownership requires verified local filesystems for storage and its control-file directories: APFS/HFS/tmpfs with the local flag on macOS, the explicitly supported local Linux filesystem types, and local fixed/removable/RAM volumes on Windows. Network, FUSE and unknown filesystems are refused before creating storage sidecars; moving a socket does not weaken database ownership. Bad or future-schema databases prevent startup and remain inspectable, never silently parked/reset or replaced. The database schema and historical records do not change as part of naming.

The default root `BERTH_HOME` / `.option-berth`, database schema/content, `oberth` executable and public Go module path do not change. Windows uses `\\.\pipe\option-berth-runtime-service`. An external client must adopt protocol 3, the new endpoint and field/check names before it can control this host.

### oberth feedback

The improvement report is opt-in and about the engine itself. A release build asks once on the first
screen (`oberth` with no arguments) when both ends of the conversation are a terminal. The answer is
recorded owner-only at `BERTH_HOME/feedback.json` (default `~/.option-berth/feedback.json`): a skip is
asked again only after an upgrade, a decision is not asked again, and `BERTH_NO_FEEDBACK` (presence
disables, the way `NO_COLOR` does) turns both the question and sending off.

| Command | What it does |
|---|---|
| `oberth feedback status [--json]` | The recorded decision, the credential `gh` holds for github.com, the channel a report would take, the local report totals, and how many reports this installation filed in the last day |
| `oberth feedback on` / `oberth feedback off` | Record the decision without waiting for the first screen; `on` names the account that will file |
| `oberth feedback report [--json] [--dry-run]` | Compose one report and file it |
| `oberth feedback log [--json]` | Each report with its issue's state, close date, labels and URL, read through `gh` |

`report` takes only the fixed vocabulary: `--kind` (`slow`, `stalled`, `coarse`, `misleading`,
`observation`, `other`), `--command` (an oberth command name), `--error-code` (a code the engine
publishes, including the ones only the CLI reports: `runtime_service_unavailable`,
`runtime_service_not_running`, `interrupted`, `timeout`, `stale` and the `attn_*` codes), and
`--observation` (`observable`,
`partial`, `blind`, `bind_denied`). `--elapsed` and `--services` are filed as buckets, not as
numbers; the build adds its own version, commit and platform.
`--note` is the reporter's own words: it is redacted, capped at 800 bytes, refused outright when it
looks like it carries a credential, and shown for confirmation unless `--yes` is passed.

With no `--kind` or `--command` on a terminal, `report` asks for them with the arrow keys from the
same fixed vocabulary, then asks for the optional note; the exact report is shown and confirmed
before it is filed. `--json`, `--yes` and a pipe keep the flag-only path, so a script never waits
for a prompt.

The report goes out through `gh issue create` with the person's own GitHub credential whenever `gh`
is installed and holds one, so that account is visible on the issue; only when there is no credential
does the command print a prefilled `issues/new` URL for the person to submit, and `--open` opens it.
Delivery is decided by `gh issue create` itself: the local probe only says whether the attempt is
worth making, so a github.com the probe cannot reach still files the issue, and a create that fails
falls back to the URL with the error that really happened. `feedback status` and `feedback on` name
that credential before any report is composed, and `feedback log` reads the filed issues through the
same login. All three read the local gh credential (`gh auth status --json hosts`)
and send nothing. The same complaint from the same build is filed at most once in 24 hours, and one
installation files at most twenty reports in a rolling day. The identity covers kind, command, error
code, observation, version and the redacted note: reports that differ in any of those are different
complaints. The local ledger at
`BERTH_HOME/feedback/reports.ndjson` records what was filed; it is advisory, and deleting it only
makes a duplicate possible again.

`status` publishes a prediction, not a promise: `channel` is the channel a report takes right now,
and it comes from the same predicate the delivery path branches on. It keeps the two kinds of history
apart: a `gh` entry is an issue that was filed, while a `url` entry is a prefilled page the person
still had to submit. The human line reads `N recorded · N filed · N prefilled`, so a prefilled page
never inflates the filed count. `--json` adds `github` (`available`, `state`, `account`, `reason`)
and `reports` (`total`, `filed`, `prefilled`, `last_at`). `state` is `signed_in`,
`credential_unconfirmed`, `signed_out`, `not_installed` or `unknown`; `available` is true when a
report is attempted through gh, so a stored credential the probe could not confirm against
github.com is `credential_unconfirmed` and still `available` — unreachable is not signed out. The
human `github` line reads `signed in as <account>`, `credential stored for <account> but github.com
is unreachable; a report is still attempted through gh`, `not signed in (...)`, or `not available
(gh is not installed)`.

`log` is the response half of the loop. For every filed report it reads the issue and prints
`open`, `resolved`, `closed as not planned`, `unknown`, or `prefilled page only`; next to the state
it prints the issue number, kind, command, version, report date, close or update date, labels and
URL. `--json` carries the same data as `github`, `summary` (the local totals plus `open`, `resolved`,
`not_planned` and `unknown`) and one entry per report with `status`, `reason` and the GitHub `issue`
object (`number`, `state`, `state_reason`, `title`, `labels`, `updated_at`, `closed_at`, `url`). The
human view shows the most recent 20 entries while `summary` counts the whole ledger. Without a gh
credential the local record still prints and every filed report says why its state is unknown;
nothing is guessed.

A report never carries a path, a log line, a manifest field, a command line, a worktree identity or an
environment variable: it is about the engine, not about the project it was run in. `--dry-run` prints
the exact title and body and sends nothing.

## Display and welcome-page details

Running `oberth` with no arguments shows the first screen: the brand mark and wordmark, a one-line
positioning statement, the local version, the three commands a work session starts with (status / up /
down), the optional capabilities a person reaches for next (`attn` and `feedback report`, in their own
group so they do not read as required) and a pointer to the full help; it is a signpost, not a manual —
the grouped command list appears only in `oberth --help`. The version line carries `(latest)` or `(latest version vX.Y.Z)` when the release
version check succeeds; when the check is unknown it prints only the local version and does not write
"not found" as "latest". When an update is found, the bottom of the first screen gives the install
command for the corresponding platform and `oberth runtime service stop`. When no improvement-report decision
is recorded yet, the screen may also ask whether to send redacted reports about oberth itself; that
question and what a report carries are specified under `oberth feedback` in Infrastructure commands.
The mark is a static generated asset in `engine/internal/cmd/mark_gen.go`, never written by hand;
the wordmark is an outline type rendered as bold text in the terminal.

The mark uses two colors per the brand's own division of labor: the two rails take ink (paper on dark
terminals) and the capsule takes terracotta. Painted in a single color, the capsule and the two rail
caps merge into three identical blocks that look like a face, so this division is part of the
semantics, not a color preference. Under truecolor it uses `#23262B` / `#F8F6F2` and `#A8543E` /
`#DB9378`, taking the latter pair when `COLORFGBG` reports a dark background; 256-color terminals take
the nearest slots (235 / 255 / 131 / 174); on 8-color terminals the rails keep the terminal's own
foreground color and the capsule degrades to yellow. Nothing is colored under `NO_COLOR`.
