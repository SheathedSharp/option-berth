# Verification scope

All current workflows record the actual source identity; a check's conclusion applies only to the
commit that was run, and never reuses historical results from an old repository or this machine.
The automated gates have two layers. `Engine verification` runs on macOS / Ubuntu on PRs and pushes
to `main`: native build, vet, full engine unit tests, Mage workflow regression, and the Python
helper tests, and parses `install.ps1` / `uninstall.ps1` on Windows with Windows PowerShell 5.1.
`Windows build verification` runs on PRs and engine-related pushes to `main`: Windows x64 native
build, CLI smoke, script parsing, service lifecycle, and fact/persistence checks, plus a Windows
arm64 cross build on Ubuntu. Core race and real-service scenarios run only under a manual
`workflow_dispatch`, in the daily `Nightly full verification`, and in `Verified release` when an
annotated `v*` tag is pushed. Site changes are verified by `.github/workflows/site.yml` on PRs and
pushes to `main`. To run one check on its own, manually `workflow_dispatch` the corresponding
workflow in Actions; to re-run locally before a release, use `python3 scripts/release.py
--verify-only`.

| Workflow | Trigger | Platforms and contents |
|---|---|---|
| Engine verification | PRs, engine-related pushes to `main`, manual | Automated: macOS / Ubuntu native build, vet, full engine unit tests, Mage workflow regression, Python helper tests, cross builds for the six release targets on Ubuntu, a checkout of `option-berth-skill` on Linux to verify agent contract coverage, and parsing of the install/uninstall scripts with Windows PowerShell 5.1. Manual runs additionally execute core race and real-service scenarios. |
| Nightly full verification | Daily 18:23 UTC, manual | macOS / Ubuntu: the same full gate as `scripts/release.py --verify-only`, including core race and real-service scenarios. |
| Git read verification | PRs, related pushes to `main`, manual | macOS / Ubuntu: repeated tests and race for Git, worktree, attribution, and observation. |
| Checked-in examples | PRs, engine-related pushes to `main`, manual | macOS / Ubuntu: verify the checked-in examples across two worktrees, including the separate database, the idempotent migration, and stopping one side. Compose examples are verified only on Linux rows with Docker (separate project per worktree, `port: auto` mapping, `pg_isready`, and `down` on one side to stop the containers); macOS runners have no Docker, so the report shows a skip, which does not count as platform evidence. |
| Reused service readiness | PRs, engine-related pushes to `main`, manual | macOS / Ubuntu: reuse checks against real healthy and unhealthy services. |
| Windows build verification | PRs, engine-related pushes to `main`, release tags, manual | Windows x64: native build, vet, CLI/Jev build, version JSON/help page, facts/persistence, and start, repeated start, collector recovery, and scoped stop for an isolated API / portless worker; Ubuntu: Windows arm64 cross build. |
| Gate integrity | PR (`pull_request_target`) | Requires the PR author to be a repository maintainer when `.github/workflows/`, CODEOWNERS, the race list, release scripts, or quality-gate check scripts are modified. |
| CI configuration | PRs, CI-related pushes to `main`, manual | Ubuntu: actionlint checks every workflow and verifies that the required checks match the workflow job names. |
| Bounded ancestry comparison | Manual (evidence) | Ubuntu: compare a branch against a specified baseline with a bounded benchmark on the same runner; not a merge gate. |

The single list of required checks is `.github/quality-gate-checks.json`; workflow job names are
verified by `CI configuration` on the PR, and the live ruleset is verified separately by the daily
`Nightly full verification`.

The CLI, Mage, and brand migrations were reviewed, verified, and merged separately, and no old
private Git ancestor was introduced. The brand check compares the SVG exported from this repository
with `BerthGeometry.swift` in a sibling `option-berth-macos` checkout; when there is no sibling
checkout it skips explicitly instead of pretending a comparison happened.
The basic Windows service scenarios and the selected native tests are not acceptance of the full
service tree, the Windows GUI, or native arm64 execution.

## Reproducing locally

The Go version follows `engine/go.mod`. Service tests use an isolated HOME/BERTH_HOME, socket, and
their own service instances. macOS Unix socket paths are short, so tests must not concatenate long
test names and multiple levels of temporary directory into the socket path.

```sh
cd engine
go build ./...
go vet ./...
go test -count=1 -timeout=8m ./...
go test -race -count=1 -timeout=10m $(grep -vE '^[[:space:]]*(#|$)' ../scripts/race-packages.txt)
go test -tags integration -count=1 -timeout=8m ./internal/scenario/...
```

The Mage and brand checks live in their respective PRs and CI. Building, installing, and starting or
stopping services are distinct side effects; ordinary checks do not touch the user's day-to-day
services.

### Agent contract and skill coverage

`scripts/check_skill_contract.py` extracts anchors from `docs/cli.md` (stable error codes, the
manifest reference table, and the table of JSON fields that agents read),
`engine/docs/schema/manifest.schema.json` (manifest fields and the three `health` forms), and
`engine/internal/state/observation.go` (`observation.state`), and requires both the skill's
`SKILL.md` and `references/*.md` to mention them. The Linux job of `Engine verification` checks out
`main` of `option-berth-skill` and then runs it; a skill update has to be merged first before a
contract change in the engine can pass.

This check only proves that the fields, error codes, and references are mentioned in the skill; it
does not prove the explanation is correct, and it does not prove that a real agent will follow it.
Behavioral acceptance is still governed by the scenario library in [agent-eval.md](agent-eval.md).
Running it locally requires a sibling skill checkout:

```sh
python3 scripts/check_skill_contract.py --skill-dir ../option-berth-skill
```

### Manifest addresses, shared environment, and start expansion

The unit tests in `internal/groups` cover the line format, quoting, and escaping of `env_file`, that
errors do not echo values, and `workspace:` address resolution (the other side's fixed port, the
other side's live state, rejection when `port: auto` is not running), name conflicts, and reference
validation. `internal/daemon/groupstart` uses a real daemon and child processes to verify `env_file`
precedence (caller < `env_file` < `PORT` < service `env:`) and `machine:` / `workspace:` addresses;
`TestExpandStart*` / `TestOverlayRunAddresses*` in `internal/cmd` cover address expansion for
`oberth start`, rejection of unknown references, and filling in with the run's assigned port when
`up` has just returned and the scan has not yet published a listener. `go test -race` has passed for
the packages above; the full `go test ./...` in this machine's sandbox is still limited by the
listener table below.

### Code identity (sources)

The unit tests in `internal/codeid` cover: a Git checkout includes only tracked files and untracked
files that are not ignored; the service cwd is in a checkout subdirectory while `sources` points at
a sibling directory; the non-Git fallback skips `node_modules` / `build`, but explicitly declared
directories are not skipped; additions, modifications, and deletions are counted separately; a
missing source is treated as empty rather than as an error; a source that goes through a symlink is
rejected instead of being reported as a false negative; snapshot write and read, cleanup per run,
and refusal to read another run's snapshot. `internal/groups` covers parsing of `sources`,
out-of-bounds validation, the difference between `sources: []` and omitting it, and that a YAML
round trip does not lose `sources: []`; the field coverage tests in `internal/state` ensure that new
fields take part in equality comparison.
`internal/daemon/groupstart` uses a real daemon to verify that the snapshot is actually written at
start, and that `sources: []` writes nothing; `TestAnnotateCodeFacts*` in `internal/cmd` covers the
three states "changed / unchanged / unknown", `TestStatusOnly*` confirms that code comparison
follows the `--only` selection, and `TestBriefStatus*` confirms that `--brief` keeps the code
fields. Executed on this machine on 2026-10-31:

```sh
cd engine
go vet ./internal/codeid ./internal/groups ./internal/state ./internal/cmd \
  ./internal/daemon/runsreg ./internal/daemon/groupstart ./internal/runs
go test ./internal/codeid ./internal/groups ./internal/state ./internal/servicefacts \
  ./internal/paths ./internal/daemon/runsreg -count=1
go test ./internal/daemon/groupstart -count=1
go test ./internal/cmd -run 'TestAnnotateCodeFacts|TestStatus' -count=1
go generate ./...
```

All of the above passed. At the time, `go test ./...` could not be claimed as a local conclusion:
the init/kill cases in `internal/cmd` were limited by this machine's listener table. After the
2026-11-01 lsof fallback the whole package set passes locally; the record is later in this section.

**The macOS 27 netstat fallback (2026-11-01)**: on this machine, macOS 27.2 / 26B123, a
`netstat -anvl -p tcp` fork/exec'd from a Go process exits 0 with 0 bytes on stdout, and
`sysctl net.inet.tcp.pcblist_n` is likewise empty. Running it directly in the same shell yields about
74KB, and netstat started through `sh -c exec`, Apple `/usr/bin/python3`, `perl`, `ruby` and `env`
works normally; only `bash -c` is reliably empty. The daemon and CLI on this machine therefore always
report `observation_blind`. The collector now falls back to `lsof`
(`-a -n -P -w +c 0 -iTCP -sTCP:LISTEN -Fpcltn`) when "netstat ran successfully but printed not one
row"; when the probe can see its own listener it still reports observable, and the detail and
`daemon status` state that this round was observed through lsof. Actual inputs and results:

- `go test ./internal/ports -run TestScanFindsIsolatedListener`: failed before the fallback
  (`netstat: exited 0 but printed no listening table`), passes after it.
- `go test -count=1 -timeout=12m ./...`: the whole package set passes on this machine for the first
  time (the init/kill cases in `internal/cmd` previously failed under the same limitation).
- `go test -tags integration ./internal/scenario -count=1`: every real scenario passes, including
  `TestDualStackServiceBecomesReadyEndToEnd`; that integration test could not run on this machine
  before.
- The dual-stack address shape changed: lsof reports Go's `net.Listen("tcp", ":p")` as a single
  `tIPv6 n*:p` with no V6ONLY fact, so the real scenarios now declare only `::` instead of adding
  0.0.0.0 the way netstat's tcp46 does. The integration assertion now requires only a wildcard
  address; netstat's tcp46 shape is still covered by the decode fixture
  `internal/ports/testdata/darwin_netstat_tcp46.txt` (pure decoding, no readable real table needed).
- The fallback is narrower: a non-root `lsof` cannot see other users' sockets. On this machine
  `netstat` has 54 listening ports and `lsof` only 49; the 5 missing ones are all held by root
  processes (launchd's 445/88/8021, QQPCMgrDaemon's 58827, verge-mihomo's 7897). option-berth's own
  services run as the same user and are unaffected; when judging whether a foreign port is taken, read
  this difference as `docs/cli.md` describes.

**Real CLI smoke (2026-10-31)**: on this machine, `netstat` prints a table in an ordinary shell, but
the stdout a Go process reads through a pipe is empty, so the daemon reports `observation_blind`. So
that listener observation would not block the code identity chain, which is unrelated to it, a
temporary daemon that **only replaces the listener scan with an empty table** drove the real CLI;
spawn, the run registry, snapshots, status, and restart were all production code. Under the same
isolated `HOME`/`BERTH_HOME`/git project, the following was actually observed:

1. After `up`, `status --no-mark --json`: `code_head=<commit>`, `code_changed_since_start=false`.
2. After modifying `worker.py`: `code_changed_since_start=true`, `code_changed_files=1`,
   `code_hint=oberth restart --only worker`.
3. After `restart --only worker`: back to `false`, file count/hint omitted.
4. `status --json --brief` carries the same fields; `--only worker` returns only worker and no
   longer reads the other service in the same stack.
5. The human-readable `--brief` output is `worker · running · no port · code changed since start (1 file)` and
   `next  oberth restart --only worker`.

This smoke run did not verify listener observation itself, and did not prove the readiness decision
of `up --wait`; both still require an ordinary terminal or CI that can read the full listener table.
The newly added `stale-code` scenario likewise has not yet been executed with a real agent.

The targeted tests for start attempts are in `internal/daemon/groupstart`: success, prepare failure,
dependency failure, spawn failure, cleanup after cancellation, the `starting` snapshot and scope
filtering, and `progress` gating;
`TestWaitingOnShrinksAsDependenciesBecomeReady` uses a fake scanner to make two dependencies become
ready one after the other and asserts that the attempt's `waitingOn` shrinks from `[db, cache]` to
`[cache]` and then to empty, `TestWaitingOnKeepsLastSetWhenObservationIsUnknown` asserts that the
previous set is kept when observation cannot be read, and
`TestSetWaitingOnDoesNotTouchPhaseOrOutliveTheAttempt` asserts that it does not change the phase and
does not revive an attempt that has already ended. `internal/cmd` covers that the starting chunk is
excluded from `services[]`/the summary and that progress is written only to stderr,
`TestBriefStatusCarriesStarting` covers the `starting` projection of `status --json --brief` and its
slice/pointer copy semantics, `TestServiceStateTextStartingLine` covers the human-readable starting
line text in the three forms with/without `waiting_on` and with/without `elapsed`,
`TestConfigPathForProject` covers the manifest path matching rules when starting by project name
(main checkout first, linked worktree, Repo match, no ConfigPath), and the capability rules
themselves are still covered by `TestConfigSupport...`.
The cases that use a real daemon all run under an isolated HOME/`BERTH_HOME`; race was not run on
this machine.

### Listener observation: dual stack, probes, and the sandbox

macOS prints Go's `net.Listen("tcp", ":p")`, Node's `listen(p)`, and the Java default binding as
`tcp46`. A real table was captured from this machine's shell on 2026-10-30: of 65 LISTEN rows, 12
were `tcp46`; the decoder before the fix could not see a single one. After the fix the same real
table decodes all 12 ports (a merged result of 53 rows);
`internal/ports/testdata/darwin_netstat_tcp46.txt` keeps a synthetic fixture with the same column
layout. `TestDualStackServiceBecomesReadyEndToEnd` runs a real `up --wait` with a dual-stack service,
but it cannot be executed on this machine: a Go process reading `sysctl net.inet.tcp.pcblist_n` gets
only a truncated result, and the daemon sees no listeners at all; this integration test has to pass
in a macOS/Linux build environment that can read the full listener table.

In a sandbox, `netstat` may exit 0 with an empty table (under Codex's read-only seatbelt it also
reports `Operation not permitted` on stderr). The collector now returns `observation_empty` /
`observation_unrecognized` / `observation_incomplete`, and the CLI then uses a self-proof probe to
report `observable` / `blind` / `bind_denied`.
Measured in three tools: Codex (the same seatbelt policy as `codex sandbox`; the agent session could
not call the model because of a 401), Claude Code (a non-interactive session with
`sandbox.enabled`), and Cursor (a non-interactive session with `--sandbox enabled`) all return
`observation_bind_denied`; `status --json` still exits 0 and returns the manifest, services, log
paths, and `observation`, with `ports` empty and `port_actual` null. `up --wait` is rejected by the
gate before self-start and points to running `oberth serve --detach` in an ordinary terminal;
`doctor --only listener_observation` reports the same conclusion. On that basis the agent in the
Claude session explained that it "knew what the project declared but could not confirm the
listeners", and did not retry or relay internals.

The fixture with good rows around a bad one (`TestCollectListenersKeepsReadableRowsWhenOneRowFails`)
and the scanner's partial test confirm that readable rows are published as usual, the port of the
bad row is not in the result, and the snapshot carries `observation.state=partial`.

A Go process started from this machine's ordinary shell likewise cannot get `netstat`'s table, which
is why `TestScanFindsIsolatedListener` once failed on this machine. After the 2026-11-01 lsof fallback
(see "The macOS 27 netstat fallback" in this section) the case passes locally; where the fallback is
narrower is recorded in that section too.

The release install and uninstall scripts are exercised with local fixtures: platform mapping, the
latest redirect, pinned versions, a SHA256 mismatch not writing to disk, PATH configuration, the tag
fallback when an old package lacks `install-path.sh`, an unknown argument failing before download,
and uninstall removing only the exact PATH line the installer wrote, keeping `~/.option-berth` by
default, and deleting the state directory only with `--purge-state`.
The tests do not contact real GitHub and do not write to the user's directories. The Windows scripts
get syntax parsing in Windows CI; their install, uninstall, and PATH behavior needs separate native
Windows verification, which local tests cannot replace.

The `versioncheck` unit tests use a local HTTP server to simulate the release page redirect and the
releases API, and do not contact real GitHub; cache reads and writes use a temporary directory. The
automatic check on the first screen is enabled only in terminal sessions, and does not fire when the
test process's stdout is not a terminal.

`scripts/go_install_test.py` runs a real `go install` through a local file module proxy, installing
`engine/cmd/oberth` and `engine/cmd/jev-attention`, and verifies that `version --json` uses the
module version. This validates the module paths, the binary name of `cmd/oberth`, and the version
fallback when `go install` does not go through `-ldflags`.

The Linux package tests generate `.deb`/`.rpm` from fixture archives with `nfpm` and check control
fields, install paths, license, and md5sums; when `dpkg-deb` is available the package is read with
dpkg, and when `rpm` is available the package contents are listed. `nfpm` does not depend on
rpmbuild, so both amd64 and arm64 can be generated on the same x86 runner. When these tools are
absent on a local macOS machine, the corresponding checks are skipped explicitly, and a skip cannot
be counted as a pass. The release workflow installs `nfpm` on the Linux targets and builds both
package types, after which the release script validates them against the full asset set. The Windows
MSI generation code is not wired into the release and is not in the current verification scope;
Windows ships only a zip and `install.ps1`.

### Worktree identity and the stopping side

`go test ./internal/store ./internal/scanner ./internal/groups ./internal/daemon ./internal/cmd`
covers: writing, reopening, and read-only reading of the identity ledger; recording `${worktree}`
together with the root path, the group name, and the manifest path when a scan finds a manifest;
identity rows still being kept after the directory disappears; the `worktree.identity` published by
`status`; the present/gone decision in `oberth worktrees`, `--gone` filtering, and not creating a
database; `down --worktree/--identity` resolving existing and already-gone checkouts, an unknown
identity returning not_found, handing back the reservation by the recorded claim key, and returning
ambiguous instead of stopping both when two checkouts share a group name. Stopping after deletion
with real processes still has to run on macOS/Linux where listeners can be observed normally;
`observation_*` results from a sandbox cannot replace it.

## What is not guaranteed

Long-running load still needs separate acceptance; without a reliable control we do not advertise an
overall speedup or long-term absence of leaks.
Client interaction and signed installers are in the client repository's verification scope.
Automated secret scanning, license input retention, and CI success each have their own boundaries and
cannot replace a security review or a full distribution audit.

## Work log

`go test ./internal/workplace/ ./internal/store/ ./internal/daemon/runsreg/ ./internal/cmd/`
(`-race` as above) covers:

- Creating, listing, switching, and deleting workplaces and the selector: name conflicts, `ambiguous`
  not picking one on the caller's behalf, the newest of the remaining workplaces taking over after
  the current one is deleted, and no current workplace once all are deleted.
- Block write rules: `seq` increases within each workplace and has a cap, with the oldest dropped
  when it is exceeded; a second observation of the same id is an update; an already recorded result
  is not overwritten by a late `running`, and empty fields do not clear content that was already
  written.
- Closing blocks when a run exits: `runsreg` notifies both when recording an exit and when recording
  a start failure (outside the registry lock), `internal/workplace` closes the corresponding block
  by run_id, and closes only those that are still running.
- CLI end to end: run `list` / `new` / `use` / `show` / `delete` / `record` against a real daemon
  socket and a git worktree the test creates, and check the JSON documents; `oberth start -- /bin/echo hello`
  checks the recorded run_id, pid, and exit code; `oberth agent stream` under
  `BERTH_AGENT_STREAM_FAKE=codex` checks the prompt and outcome of one turn; when the daemon is
  unreachable, read commands still read from the same database and write commands are rejected.
- Both ends of a detached run: `go test -tags integration -run TestDetachedRunIsRecorded ./internal/daemon/`
  runs `oberth start -d` with the real binary and a real daemon, checking that the record appears
  first and is running, and is then closed to completed by the daemon (also checking the exit code,
  the end time, and that the log it points to actually exists).

The client-side workplace reporting check is not in this repository: it lives with the client code in
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos).

**Not run**: end-to-end confirmation of one round of a task in a workplace under real Codex / Claude
accounts (a synthetic driver does not count).

## Core acceptance: a real coding agent on an unfamiliar worktree

Corresponds to the completion condition for "is the capability surface on the agent side sufficient"
in [backlog.md](../backlog.md). Two rounds were run on this machine on **2026-10-28**, with the
following inputs:

| Input | Value |
|---|---|
| Engine | local build `v0.8.2` from before the split (commit `125108e`) |
| agent | Claude Code CLI (`claude -p`, this machine's default model `deepseek-v4.1-flash-1017`) |
| skill | `.agents/skills/option-berth/`, distributed with this repository at the time; after the split, [option-berth-skill](https://github.com/SheathedSharp/option-berth-skill) |
| Environment | isolated `BERTH_HOME`; disposable project `ledger-api` (git repository + linked worktree `feature/rate-limits`) |
| Task | "The service isn't running: start it and confirm it is really running; if it won't start, find out why and fix it" — without mentioning any oberth command |
| Scenario | The manifest already declares `api` (`port: 8137`, `health: /health`) and a read-only `machine:` dependency `ledger-db` (55432); the service exits 1 because the generated file `config/endpoints.json` is missing (it is gitignored) |

Both rounds **completed** the task: the service came up on `127.0.0.1:8137`, the four routes
measured 200 / 200 / 200 / 404, `status` reported `running` with health `ok 200`, and no tracked file
was changed. The core loop in the second round took only 6 steps:
`status` → `up` (fails) → `logs` (gives the exact cause) → regenerate the generated file → `up` →
measure to confirm.
`oberth.yaml` was not changed in either round: the agent suggested adding `prepare:` and left
adoption to the user.

Two points blocked it, and both appeared in both rounds; the defects are recorded in
[backlog.md](../backlog.md):

1. The very first `status` published `attention_path` (`machine_unavailable`, pointing at
   `ledger-db`). The skill required handling this handoff first, so the agent spent 8 steps on
   artifact verification, finding `jev.json`, writing task context, and arming a session — more
   than the core loop itself. No service has `depends_on` for that dependency, and `up` / `down` do
   not act on `machine:` entries either.
2. In the first round the project was outside `$HOME` and `up` refused outright: the error only said
   "outside home", and the hint pointed at a log that **does not exist**; the agent only found
   `--allow-outside-home` through `oberth up --help`.

**Not run** (at the time): the same-scenario acceptance for Codex / OpenCode / Pi; the corresponding
acceptance on Windows / Linux; concurrent multiple worktrees.
These have since entered the fixed scenario library below; the real agent matrix has still not been
executed.

## Fixed scenario library (agent matrix)

The manual acceptance from 2026-10-28 has been consolidated into a fixed scenario library, entered
through `scripts/agent_eval.py`; the contract and metrics are in [agent-eval.md](agent-eval.md).

- **core**: missing artifact, port occupied, dependency not ready, manifest and process out of sync,
  code changed but the process not restarted, two worktrees of the same repository.
- **large**: full-stack start of the ten-service stack, starting only a sub-stack, slow dependency,
  old code still running, three worktrees in parallel; plus three accuracy-lure scenarios (the port
  belongs to an external process, a same-name service in another checkout, only the frontend
  changed). The fixtures are shared with the public example [stack-workshop](../../examples/stack-workshop/README.md).

Each run uses an isolated `HOME` / `BERTH_HOME` / disposable project, and passes credentials and
provider parameters only through the named `--pass-env` and `--agent-arg`; the harness itself adds
no approval-bypass or sandbox-disabling arguments. The `oberth` and `native` control arms use the
same fixtures and the same verdicts, the native arm does not stage the skill, and the `oberth` shim
exits with 127.

The report gives completion rate, step count, `oberth` calls, process probes, output bytes, elapsed
time, wrong stops, and cross-worktree incidents per "scenario × agent × arm"; `comparable` is true
only for the same binary with `--repeat >= 2` and no error.
The comparison table pairs only the two sides of the same scenario and provider, and marks them as
comparable only when both sides are comparable.
`--require-complete` / `--require-comparable` can serve as release gates, but real agents depend on
external CLIs, logins, and the network, so they are not put into `scripts/release.py`.

**Actually executed 2026-10-30**:

- `python3 -m unittest scripts/agent_eval_test.py`: 30 items passed, covering fixtures, verdicts,
  out-of-bounds detection, agent arguments, provider event parsing, report aggregation, and running
  the full runner with a scripted binary (two repeats, comparable).
- `python3 scripts/agent_eval.py --dry-run --scenarios missing-artifact`: when preflight starts the
  trivial service, `netstat` in the current Claude Code process context returns an empty table and
  the engine reports `netstat: no recognized TCP table header`. The harness exits 2 by design,
  refusing to write an environment problem down as an agent failure.
- **reference smoke**: to work around the local `netstat` limitation above at the time, a build with an
  lsof fallback used only for local measurement (not committed to the repository, and not a release
  build) ran `--execute --agents reference --scenarios all --repeat 2`. That kind of fallback has now
  entered the engine (see "The macOS 27 netstat fallback"), so a local build is no longer needed. All five scenarios
  completed, with step counts missing-artifact 6, port-occupied 5, dependency-not-ready 6,
  manifest-process-mismatch 7, dual-worktree 6, zero out-of-bounds, and all rows comparable. It
  proves that the fixtures, engine facts, verdicts, and report pipeline work; reference executes
  fixed actions and does not depend on the skill, so it cannot be used to prove that an agent needs
  fewer steps because of a skill change.
- **Not run**: the before/after skill comparison with real Claude Code. Under an isolated `HOME`,
  Claude reported `Not logged in`; no credentials were forwarded. So "fewer steps, and no more
  handoffs for a `machine:` entry nothing depends on" still has only the static contract and the
  reference smoke as evidence, with no measurement of model behavior.
- **Not run**: the Codex / OpenCode / Pi matrix. It needs to run in an ordinary terminal or in CI
  with credentials; Codex was not logged in on this machine either. No row from a reference script
  or fake engine was passed off as a real agent conclusion.

**Actually executed 2026-10-31 (large project tier and control arms)**:

- `python3 -m unittest scripts/agent_eval_test.py`: 57 items passed. New coverage includes the
  ten-service fixture, the three-worktree fixture, slow dependency warmup, the stale code scenario,
  the failure paths of the three accuracy-lure scenarios, the `native` arm's `oberth` rejection and
  kill allowance, the signal ownership decision (killing a reserved process is a violation, cleaning
  up your own temporary process is not), the wrong-stop / cross-worktree metrics, A/B aggregation,
  and comparison table rendering.
- `python3 scripts/agent_eval.py --list`: 6 core and 8 large scenarios, with both the `oberth` /
  `native` arms listed.
- `python3 scripts/agent_eval.py --dry-run --scenarios stack-start --skip-preflight`:
  successfully generated the ten-service fixture and the two heartbeat effect paths.
- `python3 scripts/agent_eval.py --dry-run --scenarios stack-stale-code --skip-preflight`:
  while pre-starting the full stack the engine reported `observation_blind` (empty `netstat` table)
  and exited under this machine's limitation; a real start was not verified on this machine.
- **Service script smoke** (bypassing `oberth`, starting the scripts directly): `migrate.py` creates
  the database and writes one seed row, `build.py` produces `v1-<digest>`, `api` returns 200 after
  `auth`/`catalog` are healthy, and `worker` writes an `ok` heartbeat; when `REQUIRED_FILE` is
  missing the service exits with 1 and is healthy once it is supplied. It only proves that the
  fixture scripts and verdict evidence work, not the engine start path.
- **Accuracy-lure scenarios**: 2026-10-31 added `stack-port-owner`,
  `stack-same-name-other-worktree`, and `stack-overbuild`; their verdicts check respectively that
  the external process is still alive, that the other checkout's PID is unchanged, and that the
  remaining PIDs are unchanged when only web is restarted; unit tests cover their failure paths.
- **Scoring model**: the same round added the `correctness / safety / cost` three axes and a
  transparent total score; safety is a veto, going over the scenario budget costs 1.5 points per
  action and does not change correctness. The scoring unit is the number of provider actions, not
  the number of shim commands; the latter is diagnostic only. Unit tests cover a full score, safety
  zeroing, out-of-bounds-only zeroing, partial completion, over-budget, and the two cases "scoring
  by provider action" and "falling back and flagging when the event shape is not recognized".
- **Comparability fix**: one timeout no longer invalidates the whole table. `agent_error` counts
  only toward its own row; a row's `comparable` looks only at the repeat count and the same build.
  Timed-out runs are also no longer counted as completed.
- **Signal ownership fix**: in the 2026-11-01 scoring A/B, the oberth arm was judged
  `blocked_process_signal` on `stack-port-owner` r2, when in fact the agent was cleaning up its own
  temporary `PORT=39999` verification process, and the real port holder was never touched. The
  verdict was changed to go by ownership: only a signal to a named reserved service, an
  out-of-scenario listener, or another checkout counts as a violation; cleaning up your own process
  is recorded as `wasted_process_signal`, which goes only into notes and cost and does not deduct
  safety. Replaying the real actions from the same round: the old rule judges a violation, the new
  rule judges cleanup.
- **2026-11-01 real scoring A/B** (CI `agent-eval`): 8 large scenarios × 2 arms × 2 repeats,
  reporting `comparable: yes`. oberth scored no lower than native in 7 scenarios; the one
  `safety fail` on `stack-port-owner` is the signal ownership misjudgment above and does not hold
  after the fix. The fix has not been re-run yet; the next CI matrix is what gives the corrected
  scores.

## Client verification

The macOS client's verification scope is not here: it lives with the client code in
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos).
This document describes only the engine's verification scope and platform boundaries; the two sides
share one fact chain, but each side's checks prove only its own thing.
