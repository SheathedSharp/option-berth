# Detailed verification reference

This is the current per-check and platform matrix preserved from the source baseline, not an old acceptance report. Start with [validation.md](validation.md) to choose authorized checks; use this page to look up the exact promise, input and boundary for a target. Dated execution results live separately in [history](history/validation-through-20261114.md).

Commands here describe the repository's intended disposable-host/CI checks, not permission to execute every package locally. A private HOME does not by itself make TestMain process cleanup safe. Follow the root authorization boundary before selecting any target.

All current workflows record the actual source identity; a check's conclusion applies only to the commit that was run, and never reuses historical results from an old repository or this machine. `.github/workflows/ci.yml` is the single pull-request entry point. Its `changes` job maps the changed paths to the checks they need, `ci-config` lints every workflow and validates the required-check list, and the `engine` job runs once per platform on macOS and Ubuntu: native build, vet, full engine unit tests, Mage workflow regression, the Python helper tests, the checked-in example smoke, reused service readiness, the Git read race, cross builds for the six release targets, the pinned skill contract and parsing of `install.ps1` / `uninstall.ps1` with Windows PowerShell 5.1. The `engine` job compiles `$OBERTH_BIN` once and every later step reuses it. `windows.yml` and `site.yml` are reusable workflows called from `ci.yml`, and `site.yml` deploys only outside pull requests. `main` accepts pull requests only, so a push cannot skip these checks, and the merge commit carries the tree that just passed; the gates do not run a second time on the merge. Core race and real-service scenarios run under a manual `workflow_dispatch` of `CI` and in the daily `Nightly full verification`; `Verified release` runs them when an annotated `v*` tag is pushed. To run one check on its own, `workflow_dispatch` the corresponding workflow in Actions; to re-run locally before a release, use `python3 scripts/release.py --verify-only`.

| Workflow | Trigger | Platforms and contents |
|---|---|---|
| CI | PRs, manual | The single PR entry: `changes`, `ci-config`, `engine` on macOS and Ubuntu, the reusable `windows.yml` and `site.yml`, and the advisory `Skill contract / drift from main`. Manual dispatch adds core race and real-service scenarios. `Gate / required checks` is the required check: it fails when a called check failed or was cancelled, and passes when the routing skipped one. |
| CI configuration (`ci-config` job) | PRs, manual | Ubuntu: actionlint over every workflow, a YAML parse of the workflow set, and `check_ci_gate.py` verifying that the required checks in `.github/quality-gate-checks.json` exist as job names. |
| Nightly full verification | Daily 18:23 UTC, manual | macOS / Ubuntu: the same full gate as `scripts/release.py --verify-only` including core race and real-service scenarios, plus `Nightly / observation selfcheck (os)`: it builds the CLI and runs `oberth doctor --only listener_observation` under a throwaway `BERTH_HOME`, so a runner whose collector can no longer see its own listening socket turns red. The selfcheck is not a required check. |
| Windows build verification | PRs via `CI`, release tags, manual | Windows x64: native build, vet, CLI build, version JSON/help page, facts/persistence, and start, repeated start, collector recovery, and scoped stop for an isolated API / portless worker; Ubuntu: Windows arm64 cross build. Reusable, so `CI` calls it instead of duplicating the steps. |
| Site | Called by `CI`; deploy outside PRs | Ubuntu: installs the site toolchain through the `setup-site` composite action, builds the site and runs its checks; the deploy step is disabled on pull requests. |
| Verified release | Annotated `v*` tag, manual | The full release gate and publishing, defined in [releasing.md](releasing.md). |
| Gate integrity | PR (`pull_request_target`) | Requires the PR author to be a repository maintainer when `.github/workflows/`, `.github/actions/`, `scripts/race-packages.txt`, release scripts, or quality-gate check scripts are modified. In a single-maintainer repository this guards against bot and outsider pull requests, not against a second reviewer. |
| Flake retry | `workflow_run` for `CI`, `Nightly full verification` and `Windows build verification` | Ubuntu: classifies a failed attempt with `scripts/ci_failures.py` and re-runs only infrastructure and flaky-candidate failures, at most twice. See [develop.md](develop.md) for the decision rules. |
| Repo reconcile | Daily, manual | Ubuntu: reports the three-way gate comparison from `scripts/check_ci_gate.py`, pull requests behind `main`, and required checks that never reported; carries a red run past the Actions tab by opening or commenting on one issue per workflow. It changes no ruleset, label or branch. |
| Dependabot auto-merge | Dependabot PRs (`opened`, `synchronize`, `reopened`, `ready_for_review`) | Ubuntu: reads the grouped update type with `dependabot/fetch-metadata` and turns on merge-commit auto-merge for minor and patch updates. It is not a required check and never merges by itself. Major updates stay manual; a pull request touching `.github/workflows/` or `.github/quality-gate-checks.json` gets an explanatory comment instead, because Gate integrity would wait for a maintainer author that never arrives; a failing `gh pr merge --auto` is quoted in a comment and fails the run. Needs `Allow auto-merge` in the repository settings. |
| Agent eval | Manual dispatch (`agent-eval` environment) | Ubuntu: the real-agent matrix defined in [agent-eval.md](agent-eval.md); it never runs on a pull request. |
| Bounded ancestry comparison | Manual (evidence) | Ubuntu: compare a branch against a specified baseline with a bounded benchmark on the same runner; not a merge gate. |

The single list of required checks is `.github/quality-gate-checks.json`, and it stays exactly `Gate / required checks` and `Gate integrity / maintainer-owned CI changes`. Job names are verified by `CI configuration` on the PR, the live ruleset is compared by the daily `.github/workflows/repo-reconcile.yml` run, and the whole workflow set is consolidated: 17 files became 11, with the reusable steps moved into the `setup-engine` and `setup-site` composite actions.

The CLI and Mage migrations were reviewed, verified, and merged separately, and no old private
Git ancestor was introduced.
The basic Windows service scenarios and the selected native tests are not acceptance of the full
service tree, the Windows GUI, or native arm64 execution.

Internal package paths in historical records below refer to their recorded revisions. Current host/domain paths are `internal/runtimesvc` and `internal/runtimecore`; public protocol and disk names have not migrated.

## Protocol-3 naming and storage verification

The naming audit (`scripts/check_runtime_naming.py`, also used by `runtime_naming_test.py`) rejects active retired product identifiers and explains bounded rejection/migration, SQL-history, Docker and standard-library exceptions. The per-PR smoke calls `check_runtime_naming_cli.py` against the actual candidate before service scenarios; it checks public help, usage rejection, JSON and doctor IDs without starting a host.

`scripts/check_storage_ownership.py --legacy-engine <released-v2.4.0-engine-checkout>` compiles disposable adapters against the exact unmodified released Store and the candidate Store. It verifies kernel-enforced exclusion, owner reads, failure/connection-loss/exit release and data preservation using private databases, not product services. `scripts/check_runtime_availability.py --binary <candidate> --legacy-engine <released-engine>` additionally requires macOS `sandbox-exec`: user-tree access, outbound IP and other-process inspection/signalling stay denied while the real candidate starts, refuses duplicate writers, serves storage reads, accepts subscription reconnect and stops over RPC. Neither is evidence of successful system scanning or real service-change cursor replay.

Historical execution records below predate the protocol-3 rename. Package paths and command spellings have been normalized for reproduction; their old results do **not** establish validation of the current candidate or a new release. Run only checks whose side effects are authorized in the current environment.

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

### Isolated Runtime policy and comparative checks

`internal/runtimecheck` imports production packages without their `_test.go`/`TestMain` entry points. Its collectors, PID identity/liveness/ancestry, statistics and publisher are injected; its manifests, SQLite files and locks are temporary. A fully injected in-memory scanner loop verifies independent background publication and cancellation/join, not a real runtime service or service handoff. `internal/runtimecore` pins shared readiness/generation/restart policies; `internal/attn/runner` uses fake status/streams and local fake providers. These tests neither scan/signal user services nor contact a real model. Run them under an owned temporary HOME/BERTH_HOME; they are not permission to execute whole-package runtime service/cmd TestMain on a shared development machine.

`scripts/measure_runtime.py --baseline <existing-local-commit>` archives that local engine into its own temporary directory and runs the same isolated harness on both trees. It measures one worker update across three real linked Git worktrees in a temporary repository with synthetic declarations while a fake collector is held for 15ms, plus full-status reads on a scaled 250ms attention timeline (5ms checks, 50ms candidate calibration, two changes). It also checks current/prior SQLite writer-to-read-only-reader compatibility in both directions and typed live-mirror decoding without PID probes. It executes no product binary, runtime service, Docker or remote provider, and does not clone/fetch. A failed correctness check invalidates timing; these figures do not establish real-service throughput, actual platform lifecycle behavior or an overall product speed-up.

The normal unit gate includes these packages. The shared race manifest adds the pure Runtime policy, fake-source harness and optional attention packages; existing packages and requirements are not removed. Changing that manifest still requires the repository's maintainer-owned gate. Cross-compilation and `go test -c` only establish compilation, never native execution or real service acceptance.

### Agent contract and skill coverage

`scripts/check_skill_contract.py` extracts anchors from the CLI contract (`docs/cli.md` with the
topic pages under `docs/cli/`: the stable error codes, the manifest reference table, and the table of
JSON fields that agents read),
`engine/docs/schema/manifest.schema.json` (manifest fields and the three `health` forms), and
`engine/internal/state/observation.go` (`observation.state`), and requires both the skill's
`SKILL.md` and `references/*.md` to mention them. The Linux half of the `engine` job in `CI` checks
out the literal pinned commit of `option-berth-skill`, so a push to the separate skill repository
cannot turn an unrelated pull request red and a contract change is adopted by moving the pin.

`Skill contract / drift from main` is advisory and never a required check: it runs the same check
against `main` of `option-berth-skill` with the failing step marked `continue-on-error`, and turns a
failure into a warning annotation. It proves only that the pin lags the skill's `main`; it does not
block a merge and it does not fail the job.

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
validation. `internal/runtimesvc/groupstart` uses a real runtime service and child processes to verify `env_file`
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
`internal/runtimesvc/groupstart` uses a real runtime service to verify that the snapshot is actually written at
start, and that `sources: []` writes nothing; `TestAnnotateCodeFacts*` in `internal/cmd` covers the
three states "changed / unchanged / unknown", `TestStatusOnly*` confirms that code comparison
follows the `--only` selection, and `TestBriefStatus*` confirms that `--brief` keeps the code
fields. Executed on this machine on 2026-10-31:

```sh
cd engine
go vet ./internal/codeid ./internal/groups ./internal/state ./internal/cmd \
  ./internal/runtimesvc/runsreg ./internal/runtimesvc/groupstart ./internal/runs
go test ./internal/codeid ./internal/groups ./internal/state ./internal/servicefacts \
  ./internal/paths ./internal/runtimesvc/runsreg -count=1
go test ./internal/runtimesvc/groupstart -count=1
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
works normally; only `bash -c` is reliably empty. The runtime service and CLI on this machine therefore always
report `observation_blind`. The collector now falls back to `lsof`
(`-a -n -P -w +c 0 -iTCP -sTCP:LISTEN -Fpcltn`) when "netstat ran successfully but printed not one
row"; when the probe can see its own listener it still reports observable, and the detail and
`runtime service status` state that this round was observed through lsof. Actual inputs and results:

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
  processes (launchd's 445/88/8021, QQPCMgrRuntimeService's 58827, verge-mihomo's 7897). option-berth's own
  services run as the same user and are unaffected; when judging whether a foreign port is taken, read
  this difference as `docs/cli.md` describes.

**`init` degrades when it cannot look (2026-11-02)**: a failed scan no longer aborts `init`. The
starter's comment says listening is unknown, with the observation code and the next step, instead of
"Nothing was listening". Run on this machine, macOS 27.2 / 26B123, in an isolated `HOME`/`BERTH_HOME`
with a throwaway git project that runs no services:

- With both collectors made blind — an `exit 0` `netstat` and an `exit 1` `lsof` on `PATH` —
  `doctor --only listener_observation` reports
  `blind (… netstat: exited 0 but printed no listening table; fallback: lsof: printed no listening
  table (exit exit status 1))`. In the same process `init` exits 0 and writes `oberth.yaml` with the
  comment `Listening inside this project is unknown` plus `observation_blind` and the hint, one
  stderr note, and `init --dry-run --json` still parses.
- With the shims removed (this machine then takes A1's netstat→lsof fallback) the comment returns to
  `Nothing was listening inside this project` and stderr says `0 listening ports`.
- `go test ./internal/cmd ./internal/groups ./internal/draft ./internal/runtimesvc/...` passes. New
  cases: `TestInitSaysListeningIsUnknownWhenTheScanFails`, `TestInitDraftRunsWhenTheScanIsBlind`,
  `TestProposeSaysListeningIsUnknownWhenTheScanFailed`,
  `TestProposePartialObservationKeepsTheIdleSentence`, `TestBriefSaysTheListenerTableIsUnknown`.

**The whole chain on macOS 27 (2026-11-02)**: same machine, a fresh isolated `HOME`/`BERTH_HOME` (the
example project lives inside `HOME`, because the engine refuses to start commands outside it), using
the `oberth` built from this branch:

- `doctor --only listener_observation` → `ok  this process can see its own listener`.
- `runtime service run --detach`, then `runtime service status --json`'s `observation` →
  `{"state":"observable","detail":"saw this process's listener on 127.0.0.1:60344 (pid 32739) via lsof"}`.
  The `via lsof` in the detail is A1's collector source; no extra JSON field was added.
- `examples/hello-worktree`: `up --wait --json` brings both services up as `started`/`ready` (api on
  the assigned port 28031, the worker portless); `status --json` lists that port and reports
  `observation: null` for a complete table; `down --json` stops both by pid/sigterm and reports
  `released: 1`. A first attempt with the example outside `HOME` was refused with
  `start_failed … is outside …/home` — the engine's existing safety boundary, not an observation
  problem — and passed once moved inside. An earlier attempt that shared one `BERTH_HOME` with that
  failed run made `down` report `ambiguous: group hello-worktree is shared by wt_…, wt_…`; a fresh
  `BERTH_HOME` cleared it, so it was leftover registration of the same directory, not this change.
- `go test -tags integration ./internal/scenario/... -count=1` → passes (63.6s).
- `runtime service stop` afterwards; temporary directories collected; no runtime service left running.

Not covered here: the runtime service's `groups.init` is unchanged. Its scan failure already returns an
`observation_*` RPC error rather than an empty table, so it has no "empty table read as idle" path,
and the CLI's `oberth init` does not go through the runtime.service.

**Narrow-table coverage (2026-11-04)**: the lsof fallback still answers "can this process see its own
listener", but it can no longer be read as "this port is free". Same machine (macOS 27.2 / 26B123),
commits `94a90f6` and `9d863f1`, isolated `HOME`/`BERTH_HOME`, throwaway git project declaring
`machine:` 445 (launchd), 12101 (another user) and 6379 (this user's redis):

- `status --json --no-runtime-service` → `observation: {"state":"observable","source":"lsof",…}`; 445 and
  12101 are `"listening": false, "listening_known": false`; 6379 is `"listening": true,
  "listening_known": true`.
- `status` → `listeners limited · …`, `depends on ldap 445 listening unknown · qq 12101 listening
  unknown · redis 6379 listening`.
- `runtime service run --detach`, then `runtime service status --json` → `observation.source: "lsof"` with the probe detail;
  `status --json` through the runtime service returns the same three rows; `runtime service stop` afterwards, no runtime service
  left running.
- `go test -count=1 -timeout=8m ./...` → clean. New cases:
  `TestNarrowScanPublishesItsCollectorAndUnknownMachinePorts`,
  `TestMachineReferencesPublishUnknownUnderANarrowTable`,
  `TestScanGroupsObservedPublishesNarrowCoverage`,
  `TestStatusPrintsUnknownForANarrowMachineReference`,
  `TestNarrowTableVerifiesCandidatesByBinding`, `TestNarrowTableWithoutABindProbeFailsClosed`,
  `TestCanBindPortSeesAnExistingListener`.

Not run: a real `up --wait` with `port: auto` on a narrow table, and a second-user listener holding a
port inside the claim range. The bind probe was measured against a real listener in the same process;
the claim path is covered by the tests above, not by a real foreign holder.

**The attention scenario tests after the consent gate (2026-11-02)**: gating the artifact on consent
left two integration tests asserting a contract that no longer exists, and `main`'s nightly went red
on both platforms (run `41355765757`, headSha `79b5c33`). Both failures are the tests' fault, not the
gate's:

- `TestAttentionFailureMatrix` and `TestAttentionManifestConflictIsNotActionable` never wrote
  `jev.json`, so `status` correctly published no `attention_path` and the tests waited out their
  budget for one that now only exists after `oberth jev enable`. Both now call a helper that writes
  the owner-only settings file into the run's isolated `HOME` before the fixture starts. The key is a
  placeholder: these tests derive the artifact from runtime facts and never call a provider
  (an injectable HTTP server covers Jev itself).
- `TestAttentionNormalAndUndeclaredBoundaries` still expected an `undeclared_listener` event. That
  kind was removed on purpose — an undeclared listener is a fact `status` already reports with its
  port and owner, so the test now asserts the port appears while no artifact is created.

Verified on this machine (macOS 27.2 / 26B123), all three re-run against this branch:

- `go test -tags integration -count=1 -run 'TestAttention' ./internal/scenario/` → passes.
- The same command with the settings-file helper removed from the failure matrix → fails in 17s, so
  the helper is what the passing case depends on, not a coincidental green.
- `go test -tags integration -count=1 ./internal/scenario/` → passes (48.4s), the whole real-service
  suite rather than the three attention cases alone.
- `go test ./internal/ports/... ./internal/runtimesvc/... ./internal/cmd -count=1` → passes.
- `scripts/verify-attention-handoff.py --choice continue_without_action` → `status: passed`.

Not run: the nightly workflow itself, and `-race` on the scenario package.

**The optional-Jev hint was unreachable (2026-11-02)**: `status` never showed the one-line offer of
the enhancement. The hint required "Jev off **and** an `attention_path`", but the consent gate makes
the artifact exist only when Jev is on, so the two halves could not hold at once. The contract text
in [cli/facts.md](../cli/facts.md) "Listener observation and the optional Jev enhancement" described the hint as
working; the code silently did not.

Deriving and publishing are now two decisions. `publishAttention` always runs the deterministic
classification and records the verdict in an unexported field, then publishes only if the person
consented. The hint reads that verdict, so it fires exactly when there is something to assess and the
enhancement is off — and still not on `--json`, `--brief`, a non-TTY, `BERTH_NO_TIPS=1`, or within
the interval.

Actual inputs and results on this machine (macOS 27.2 / 26B123, isolated `HOME`/`BERTH_HOME`,
throwaway git project whose only service exits 1):

- `status --json` → `attention_path: null`, `last_exit.reason: crashed`, `exits: ["crashed"]`. This is
  the state that used to be unreachable: a derived anomaly with nothing published.
- `status` under a real PTY (`pty.fork`) → the table, then
  "Jev: an optional model can assess this anomaly; `oberth jev enable` turns it on".
- `status --json` and `status --brief` under the same PTY → tip absent.
- `BERTH_NO_TIPS=1 status` under the same PTY → tip absent.
- After writing `jev.json` with `enabled: true`, `status` under the PTY → tip absent and
  `attention_path` published, which is the intended "already opted in, nothing to offer" case.
- `go test ./...` → clean. New cases: `TestTipFiresWithJevOffAndAnAnomaly`,
  `TestAnomalyVerdictSurvivesTheConsentGate`. Deleting the `doc.attentionEvent = true` assignment
  makes the second fail, so the assertion is load-bearing rather than incidental.
- `go vet ./...` → clean (with `GOPROXY=https://goproxy.cn,direct`; the default proxy timed out on a
  test-only dependency).

Not run: `-race` on `internal/cmd`.

**Real CLI smoke (2026-10-31)**: on this machine, `netstat` prints a table in an ordinary shell, but
the stdout a Go process reads through a pipe is empty, so the runtime service reports `observation_blind`. So
that listener observation would not block the code identity chain, which is unrelated to it, a
temporary runtime service that **only replaces the listener scan with an empty table** drove the real CLI;
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

The targeted tests for start attempts are in `internal/runtimesvc/groupstart`: success, prepare failure,
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
The cases that use a real runtime service all run under an isolated HOME/`BERTH_HOME`; race was not run on
this machine.

### Listener observation: dual stack, probes, and the sandbox

macOS prints Go's `net.Listen("tcp", ":p")`, Node's `listen(p)`, and the Java default binding as
`tcp46`. A real table was captured from this machine's shell on 2026-10-30: of 65 LISTEN rows, 12
were `tcp46`; the decoder before the fix could not see a single one. After the fix the same real
table decodes all 12 ports (a merged result of 53 rows);
`internal/ports/testdata/darwin_netstat_tcp46.txt` keeps a synthetic fixture with the same column
layout. `TestDualStackServiceBecomesReadyEndToEnd` runs a real `up --wait` with a dual-stack service,
but it cannot be executed on this machine: a Go process reading `sysctl net.inet.tcp.pcblist_n` gets
only a truncated result, and the runtime service sees no listeners at all; this integration test has to pass
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
gate before self-start and points to running `oberth runtime service run --detach` in an ordinary terminal;
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
`scripts/install_test.py` also covers the user-level service unit: the plist or unit file names the
installed binary, passes `--persistent` and `BERTH_RUNTIME_SERVICE_DETACHED`, and carries the
keepalive or restart settings; the enable step ran; `--no-service` and its environment variable write
no unit; a failing observation check skips registration; a refusing supervisor only warns; an upgrade
stops the previous unit while the old binary is still on disk and starts the new one afterwards; and
uninstall removes the unit before anything else. Those tests use a temporary `HOME` and fake
`launchctl`/`systemctl` placed first on `PATH`, so no real supervisor runs and nothing is installed.
`internal/usersvc` is tested the same way: the exact argv per platform, the `gui/<uid>` → `user/<uid>`
fallback, the installed and active states, and the error when the supervisor is missing.
The tests do not contact real GitHub and do not write to the user's directories. The Windows scripts
get syntax parsing in Windows CI; their install, uninstall, and PATH behavior needs separate native
Windows verification, which local tests cannot replace. No test loads a real launchd or systemd user
unit either; that first load is verified by hand, not in CI.

The `versioncheck` unit tests use a local HTTP server to simulate the release page redirect and the
releases API, and do not contact real GitHub; cache reads and writes use a temporary directory. The
automatic check on the first screen is enabled only in terminal sessions, and does not fire when the
test process's stdout is not a terminal. They separate the three ways a check can fail — a server that
never answers, a connection that is refused, and a server that answers with 500 — and prove that only
the first is retried on the short window. They do not measure how long a real release feed takes, so
the two budgets are pinned as documented values rather than derived from a measurement.

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

`go test ./internal/store ./internal/scanner ./internal/groups ./internal/runtimesvc ./internal/cmd`
covers: writing, reopening, and read-only reading of the identity ledger; recording `${worktree}`
together with the root path, the group name, and the manifest path when a scan finds a manifest;
identity rows still being kept after the directory disappears; the `worktree.identity` published by
`status`; the present/gone decision in `oberth worktrees`, `--gone` filtering, and not creating a
database; `down --worktree/--identity` resolving existing and already-gone checkouts, an unknown
identity returning not_found, handing back the reservation by the recorded claim key, and returning
ambiguous instead of stopping both when two checkouts share a group name. Stopping after deletion
with real processes still has to run on macOS/Linux where listeners can be observed normally;
`observation_*` results from a sandbox cannot replace it.

### The doctor's agent-skill row

`internal/doctor`'s `checkSkillVersion` only reads the `SKILL.md` of the skill already installed in
three places under `HOME`; it does not go online, dial the runtime service, or install and change anything.
Unit tests (`checks_skill_test.go`) cover: a nested `metadata.version` is not confused with a
top-level `version:`; missing frontmatter, unclosed frontmatter, a missing version field and an empty
value each report an error instead of guessing a version; nothing installed is `skip`; a single copy
is always `ok` and carries no `fix`; two copies at the same version are `ok`; two copies at different
versions `warn` and name both versions in `fix`; a copy whose frontmatter cannot be read is not a
version conflict and is only marked `unreadable` in the detail; a symlink into a live checkout is
called a `link`; a dangling symlink `warn`s and prints the original target; `--only agent_skill` runs
just this row; and the check never dials the runtime.service.

Executed on this machine (macOS 27.2 / 26B123) on 2026-11-01:

```sh
cd engine
go build ./... && go vet ./internal/doctor/
go test ./internal/doctor/ -count=1
```

All of the above passed. The real CLI was also run with an isolated `HOME`/`BERTH_HOME` (a locally
built binary, deleted afterwards):

| Input | `doctor --only agent_skill` |
|---|---|
| nothing installed | `skip`, "the agent skill is not installed here" |
| `~/.claude/.../SKILL.md` declaring 0.7.1 | `ok`, "the installed skill copies agree" |
| plus `~/.codex/.../SKILL.md` declaring 0.6.0 | `warn`, "2 installed copies of the skill disagree on its version", with 0.7.1 vs 0.6.0 in the fix |
| `~/.codex/...` replaced by a link to a directory that does not exist | `warn`, "a skill link points at a checkout that is gone", with the original target in the fix |

Under `--json` the row is still `id`/`status`/`summary`/`detail`/`fixable`; no field was added.

**Not executed**: a copy installed through the plugin marketplace is not verified. The plugin install
path is managed by the client (Claude Code's `.claude-plugin`) and is not one of those three user
skill directories; this row reports `skip` for it, which must not be read as "the skill is not
installed". Nothing here compares against an "official latest version" either: with no network the
engine cannot prove which copy is current, which is why a single copy is never called outdated.

## What is not guaranteed

Long-running load still needs separate acceptance; without a reliable control we do not advertise an
overall speedup or long-term absence of leaks.
Signed installers and GUI interaction are outside this repository's verification scope.
Automated secret scanning, license input retention, and CI success each have their own boundaries and
cannot replace a security review or a full distribution audit.
