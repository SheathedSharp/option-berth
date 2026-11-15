# Historical verification records through 2026-11-14

These are earlier observations, not acceptance of the current revision. Commands/package names may have been normalized by earlier edits. Do not execute them without an isolated authorized environment. Current checks: [verification guide](../validation.md).

## Work log

`go test ./internal/workplace/ ./internal/store/ ./internal/runtimesvc/runsreg/ ./internal/cmd/`
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
- CLI end to end: run `list` / `new` / `use` / `show` / `delete` / `record` against a real runtime service
  socket and a git worktree the test creates, and check the JSON documents; `oberth start -- /bin/echo hello`
  checks the recorded run_id, pid, and exit code; `oberth agent stream` under
  `BERTH_AGENT_STREAM_FAKE=codex` checks the prompt and outcome of one turn; when the runtime service is
  unreachable, read commands still read from the same database and write commands are rejected.
- Both ends of a detached run: `go test -tags integration -run TestDetachedRunIsRecorded ./internal/runtimesvc/`
  runs `oberth start -d` with the real binary and a real runtime service, checking that the record appears
  first and is running, and is then closed to completed by the runtime service (also checking the exit code,
  the end time, and that the log it points to actually exists).

The client-side workplace reporting check is not in this repository: it lives with the client code in
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos).

**Not run**: end-to-end confirmation of one round of a task in a workplace under real Codex / Claude
accounts (a synthetic driver does not count).

## Core acceptance: a real coding agent on an unfamiliar worktree

Corresponds to the completion condition for "is the capability surface on the agent side sufficient"
in [backlog.md](../../backlog.md). Two rounds were run on this machine on **2026-10-28**, with the
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
[backlog.md](../../backlog.md):

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
through `scripts/agent_eval.py`; the contract and metrics are in [agent-eval.md](../agent-eval.md).

- **core**: missing artifact, port occupied, dependency not ready, manifest and process out of sync,
  code changed but the process not restarted, two worktrees of the same repository.
- **large**: full-stack start of the ten-service stack, starting only a sub-stack, slow dependency,
  old code still running, three worktrees in parallel; plus three accuracy-lure scenarios (the port
  belongs to an external process, a same-name service in another checkout, only the frontend
  changed). The sample and its scripts are shared with [stack-workshop](../../../examples/stack-workshop/README.md).

Each run uses an isolated `HOME` / `BERTH_HOME` / disposable project, and passes credentials and
provider parameters only through the named `--pass-env` and `--agent-arg`; the harness itself adds
no approval-bypass or sandbox-disabling arguments. The `oberth` and `native` modes use the same
sample and the same verdicts, the native mode does not stage the skill, and the `oberth` shim
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

## Skill arm and intervals (2026-11-01)

The harness gained a third arm, `oberth-noskill`, and interval columns. This section records what
was actually run on this machine; the CI matrix below the change is not implied.

- `python3 -m unittest scripts.agent_eval_test`: 67 items passed. New coverage: the skill arm keeps
  the whole `oberth` safety policy (`kill`, `oberth kill`), the skill arm execs the real binary
  instead of the native arm's 127 stub, `compare_arms(treated, control)` pairs
  `oberth` against `oberth-noskill` rather than against `native`, a pair missing its control side
  produces no row, intervals stay `null` below 5 valid runs while the point estimates stay, a wide
  interval reports `completion_intervals_disjoint: false`, a clean separation reports `true`, and a
  short row reports `null` instead of `false`.
- `python3 -m unittest discover -s scripts -p '*_test.py'`: 164 items passed, 1 skipped (the brand
  check skips without a sibling `option-berth-macos` checkout).
- `python3 scripts/agent_eval.py --list`: lists `oberth`, `oberth-noskill`, `native` with the new
  description line.
- `python3 scripts/agent_eval.py --dry-run --suite core` (isolated `HOME` / `BERTH_HOME` under
  `/tmp`): preflight ok through the lsof fallback, all 6 core fixtures provisioned.
- `python3 scripts/agent_eval.py --execute --agents reference --suite core --arms oberth --repeat 2`:
  12 runs, all completed, zero boundary violations, all rows comparable; the report carries
  `compare_arms: ["oberth", "native"]` and the note that `--repeat 2` is below the interval
  threshold.
- `python3 scripts/agent_eval.py --dry-run --suite large`: preflight ok, all 8 large fixtures
  provisioned, up to 24 ports and 42 files across three worktrees.
- `python3 scripts/check_docs.py` and `python3 scripts/check_ci_gate.py`: clean.
- **Not run**: a real agent on the `oberth-noskill` arm. Its correctness depends on the agent not
  discovering the skill another way, which the staging code prevents but only a real run can show.
  The baseline rerun that would produce that comparison was triggered on `main` but had not been
  approved when this was written.

## Manual runs: real JVM / Node / Compose (2026-11-01)

The complex sample is Python plus one Postgres container. These three runs checked that the engine also
drives real toolchains; they were manual, not part of the benchmark matrix. The Compose part has since
become the `db` service of the [stack-workshop](../../../examples/stack-workshop/README.md) sample, which
the manual benchmark uses; it is not part of the per-PR Examples check.

Environment: macOS 27.2 / 26B123, arm64; OpenJDK 21.0.12; Maven 3.9; Node 26.9.0; Docker 29.8.2
through colima, Compose 5.5.1. Every run used a throwaway `HOME` / `BERTH_HOME` under `/tmp` and a
disposable git repository, and every runtime service and container was removed at the end.

- **Maven / JVM**: `pom.xml` with `maven-jar-plugin` and a `Main-Class`; the service is a
  `com.sun.net.httpserver.HttpServer` answering `/health`. Manifest:
  `prepare: mvn -q package -DskipTests`, `cmd: java -jar target/demo-1.0.0.jar`, `port: auto`,
  `health: /health`. `oberth up --wait --wait-timeout 180s` reported
  `prepare 2417ms`, `spawning 2418ms`, then `✓ api http://localhost:10870`; `status` showed
  `running · listening on 10870`, `curl /health` returned `{"status":"ok"}`, and `down` stopped it
  and released the claim. A first attempt failed with `no main manifest attribute`: the fixture was
  wrong, not the engine.
- **Node**: `npm run build` writes `dist/index.html` from `src/app.html`, `node server.js` serves it
  with `/health`; `dist/` is gitignored. `up --wait` reported `prepare 440ms`, `spawning 443ms`,
  `✓ web http://localhost:19670`. After editing `src/app.html`, `status` reported
  `code changed since start (1 file)`; `down` + `up --wait` rebuilt and served the new content.
- **Compose**: the standalone `compose-stack` example of this run (`postgres:16-alpine`, `port: auto`,
  `pg_isready` health command), which has since become the `db` service of the checked-in
  [stack-workshop](../../../examples/stack-workshop/README.md) fixture. Two environment preconditions had
  to be met before it could run, and both are properties of this machine rather than of the example:
  the isolated `HOME` hid `~/.docker/cli-plugins`, so `docker compose` degraded to `docker` + an
  unknown subcommand (exit 125); and the colima context needs
  `DOCKER_HOST=unix://$HOME/.colima/default/docker.sock`, without which the container client cannot
  reach the API. With both handled, the container started and `compose exec -T db pg_isready`
  answered `accepting connections`, but `oberth up --wait` still reported
  `timed out after 30s waiting for db to be ready` while `status` showed `auto port unassigned`
  and the container was healthy. **This one is an open gap on this machine**: the standalone example
  reached ready in the Linux Examples job, but this colima host did not, and the cause has not been
  isolated (the health command and the container both work when driven by hand). It is recorded as a
  failure, not as a pass.
- **Not run**: the `native` arm on any of these three, and any real coding agent on them. They prove
  the engine drives real toolchains; they say nothing about agent behaviour.

## A matrix that measured nothing (2026-11-02)

The 8 `large` scenarios × 2 arms × 5 repeats matrix was dispatched on `main` (`691f619`) and
completed in 32 minutes. Every number in it is void, and the reason is worth keeping because it was
recorded as a clean run.

Actual inputs and results:

- `gh workflow run agent-eval.yml -f agents=claude -f suite=large -f repeat=5` → run
  [41411562913](https://github.com/SheathedSharp/option-berth/actions/runs/41411562913), `plan` 1 min,
  `claude` 32 min, `merge` 1 min; `comparable: true`, 80 runs, 5 repeats each, all
  scenario/agent/arm triples present.
- Of those 80 runs, **70 were `not_completed` and 10 were `ok`**, and the 10 were all
  `stack-port-owner` — the one scenario whose correct answer is "do nothing, hand the decision
  back". Every run recorded `agent_error: None`.
- The completion rate was **0% for both arms in 7 of 8 scenarios**, and the comparison table
  therefore reported `completion_delta_pp: 0.0` everywhere with no disjoint intervals — a table that
  reads as "the product does not help" when in fact no agent ran.
- The agent artifact says why: `agent.stdout.jsonl` ends with
  `{"type":"result","is_error":true,"result":"API Error: 400 upstream 400: Missing required parameter:
  'messages[2].reasoning_content'. This model needs the reasoning of every earlier assistant
  message..."}`. Turn one succeeded, the tool result went back, and the provider rejected turn two.
  Median run duration was 21 s and `git_changes` was empty in every row.

Two independent defects let that through, and both are the same mistake in different places — the
harness preferred its own inference over the agent's own report:

- `parse_claude` read the `result` event's text and dropped its `is_error` flag, so a hard provider
  failure was stored as the agent's final answer.
- `run_agent` only raised an error when `returncode != 0 and not steps`, so a run that died *after*
  acting recorded no error at all. The 8 tool calls it made before dying suppressed the only signal
  the harness looked at.
- Separately, the gate that was supposed to prevent this — `check_agent_gateway.py` — sends **one**
  request with no history. It passed, correctly: the endpoint accepts the model name. It cannot
  observe a requirement that only appears on the second request, and it reimplements the client
  instead of running it.

What changed (code, not procedure):

- `AgentTrace` replaces the `(steps, final)` tuple every parser returned. The agent's own error
  verdict travels in the same value as the fields a caller scores, so a reader cannot take the
  numbers and silently drop the diagnosis. `parse_claude` / `parse_codex` / `parse_generic` each read
  their own dialect's error event; `run_agent` prefers `trace.error` over any exit-code inference.
- `agent_eval.py --smoke` runs one requested agent on `missing-artifact` through the ordinary
  runner, and fails when the agent reports an error **or takes no action at all**. Taking an action
  is what proves the model closed a tool round trip — the part a single request cannot reach.
- The workflow gate is that smoke, and it runs inside each `matrix` shard using **the same `common`
  argument array the matrix itself uses**. A preflight assembled from its own flags tests a
  different configuration from the one that runs, which is how a broken matrix passes its gate. The
  gateway script stays as a manual diagnostic and is no longer the gate.

Verified on this machine:

- `python3 -m unittest scripts.agent_eval_test` → 74 items pass (was 67). New:
  `test_claude_error_result_is_a_failure_not_an_answer`, `test_codex_failed_turn_is_a_failure`,
  `test_generic_error_event_is_a_failure`, `test_a_clean_result_reports_no_error`, and three
  `SmokeModeTests` that drive `main(["--smoke", ...])` with a scripted agent.
- Negative control: deleting the `is_error` branch from `parse_claude` fails
  `test_claude_error_result_is_a_failure_not_an_answer` and one smoke case, so the assertions are
  load-bearing rather than incidental.
- `python3 -m unittest discover -s scripts -p '*_test.py'` → 231 pass, 1 skipped (the brand check
  needs the client checkout).
- `python3 scripts/check_docs.py` → 6 pairs consistent.

**Not run**: the corrected matrix. The workflow change needs this PR to merge before a dispatch uses
it, and a real `--smoke` costs one model turn per agent. Whether `reflection/beam:free` now completes
a multi-turn loop *in CI* is exactly what that smoke will answer first.

## Cold start: no manifest (2026-11-02)

The cold-start suite is the first one whose fixture has no `oberth.yaml`. This section records what
was actually run on this machine (macOS 27, arm64, Go `1.27.1`); the CI matrix is not implied.

- `python3 -m unittest scripts.agent_eval_cold_test`: 42 items passed. Coverage: registration and
  suite selection, the three fixtures provision with no manifest and committed executable scripts, the
  services read `PORT` rather than a baked-in number, the shipped tools really run (compose `setup`
  renders the env file and `up` refuses before it exists, the node install step writes the member
  marker, `run-npm` resolves a workspace script, `mvnw` refuses an unknown module and `-am` builds
  both libraries), adoption is gated by `init adopt --dry-run` and never writes a rejected draft, and
  the judge fails on a missing or empty manifest, an undeclared service, a port mismatch, a 503 health
  answer, an edited tracked file and rewritten Git history while tolerating a manifest write.
- `python3 -m unittest scripts.agent_eval_test`: 67 items passed — the existing suites are unchanged
  by the new tier and by the `--suite` choice list.
- `python3 scripts/agent_eval.py --list`: the three cold scenarios are listed under their own tier
  alongside the 6 core and 8 large ones.
- `python3 scripts/agent_eval.py --dry-run --suite cold --skip-preflight` (isolated `HOME` /
  `BERTH_HOME` under `/tmp`): all three fixtures provisioned — compose 8 files, node 11, maven 12.
- **Reference flow** against a `go build` of this worktree (`dev/unknown`, sha256 `6e157e0c…`):
  `--execute --agents reference --suite cold --repeat 2 --require-comparable` produced 6 runs, all
  `completed=True`, zero boundary violations, all rows comparable. Step counts 9 / 15 / 13 for
  compose / node / maven, median duration 4.7s / 5.0s / 3.5s. The report carried the `cold_start`
  block with 3 scenarios × 2 observations × 2 drafting runs = 12 agent calls.
- The reference arm adopts a manifest it did not draft, so `adopted_by_harness` is 0 in that run and
  the rows say nothing about draft quality. The harness-adoption path is covered by unit tests with a
  scripted binary, not by a real drafting agent.
- **Not run**: a real coding agent on any cold-start scenario, so there is no baseline success rate
  yet and no threshold is set. The `native` control arm on cold start is also not run; whether an
  agent without `oberth` can reach a healthy stack by reading these READMEs is untested. Both need the
  `agent-eval` environment a human approves.
- **Not run**: the CI path. The workflow gained a cold-fixture provisioning step and the `cold` value
  for `suite`; neither has executed on a runner.
- The `--binary` was a plain `go build`, which reports `version: dev`: the rows are self-consistent
  but not comparable to a released build.

## Residual cleanup acceptance: crash, mutation, and startup (2026-11-11)

Source identity: branch `test/acceptance-final-20261111` at `5ecf1f23`, which merges `main` at
`589b17a5` (PR #270). The documentation commits after `5ecf1f23` change no compiled file, so the
measured binary is also the binary of the branch head. Baseline for the startup comparison is
`ec5dbac9`, the base named by the residual-cleanup plan. Both binaries were built with
`CGO_ENABLED=0 go build -trimpath -buildvcs=false`: candidate sha256
`f37a3221b97424b9556a96cce31c4b8c564cd5c096ad0df59f348878b6dc869c`, baseline sha256
`c171e90473117ca818d12c347f65a4f956a0bb20e461207b4a4bc91c89a94730`. The machine was macOS 27.2.1
arm64 (host hash `b009fd81c24a`), Go 1.27.1 darwin/arm64.

### Static and package checks

| Check | Command | Result |
|---|---|---|
| Vet | `go vet ./...` and `go vet -tags integration ./...` | exit 0 |
| Staticcheck | `staticcheck ./internal/testenv ./internal/store` | no findings |
| Duplication | `dupl -t 100` on `store`, `testenv`, `killer`, `scenario`, `runtime service` | 2 pre-existing clone pairs, identical to `main` |
| Full tests | `go test -count=1 -timeout=20m ./...` | all packages `ok` |
| Race | `go test -race -count=1 ./internal/store ./internal/testenv ./internal/killer ./internal/runtimesvc` | all packages `ok` |
| Integration | `go test -tags integration -count=1 ./internal/runtimesvc/... ./internal/scenario` | all packages `ok`, no stray `runtime service run` process |
| Cross build | `GOOS={linux,windows} GOARCH={amd64,arm64} go build ./...` and `go vet` on the changed packages | exit 0 for all four targets; not native execution |

`staticcheck ./internal/killer ./internal/runtimesvc` still reports pre-existing `U1000` findings
(`signalTree`, `fastStats`, `setProbe`, `defaultSocketPath`); they are unchanged from `main` and not
part of this acceptance.

### Process cleanup ownership

Cleanup may only signal a process the run still owns. Three paths were narrowed:

- `testenv.KillIfMatches` re-reads the pid with `ps eww -p <pid> -o command=` and applies the same
  ownership predicate immediately before signalling; pid 1 and the test process are never candidates.
  `reportAndKill` and the scenario socket sweep use it, and `TestKillIfMatchesRechecksTheProcessBeforeSignalling`
  plus `TestKillIfMatchesStopsARealProcess` pin the decision and the real re-read.
- The killer fixture starts its shell with `Setpgid` and cleans up the held process group; the old
  fresh-table descendant walk (`killTreeNow`) is gone. The wedged-listener fixture follows the same
  rule. `go test ./internal/killer` passes.
- `runtime service`'s `killTree` refuses numeric-pid signals once `cmd.ProcessState` is set: a waited-on pid may
  already belong to another process, and there is nothing of that command left to stop.

The integration suites above start real runtime services and services, then assert no `runtime service run` process survives;
the measurement harness additionally re-reads each candidate pid before any signal and recorded no
stray in either phase.

### Mutation of `internal/store/store.go`

The harness is the repository's AST single-token mutator (`gen.go`) with `go test -overlay`,
`-count=1`, `-failfast`, `-timeout=120s`, the whole `./internal/store` package, and four workers.
It is evidence tooling, not a product script. Fifty-four samples:

| Classification | Count |
|---|---:|
| assertion | 45 |
| panic | 9 |
| survived | 0 |
| timeout | 0 |
| compile / tool | 0 / 0 |

The nine panics are mutation-induced illegal states (for example `textColumn` returning
`nil, nil`); they are not counted as assertion kills. The three timeouts of the earlier run all came
from a leaked or starved connection: `Store.Close` waited forever for a connection a mutated
`checkpointAndLeaveWAL` had leaked (two samples), and the concurrent reader/writer test had no
deadline when `textColumn` leaked rows and exhausted the pool (one sample). `Close` now bounds the
checkpoint with a 5s context, and the concurrent test waits with a 10s deadline instead of hanging
until the package timeout. One survivor was a Windows drive-letter branch unreachable on darwin; it
was made testable by extracting `readOnlyURIPath` and is now killed. No survivor remains.

### Cold and warm startup

Protocol: project is `examples/hello-worktree`, copied under a fresh isolated `HOME`; the socket is
under a short `/tmp` path. Cold = fresh `HOME`, no runtime service, no database, no reservations; the measured
command is the first `oberth up --wait --json`, so runtime service startup is inside it. Warm = one `HOME` for
the phase, runtime service started once and kept, services brought up and stopped once unmeasured; the measured
command is `up --wait --json` with no service running. Cleanup used only the binary's own `down` and
`runtime service stop` against that `HOME`/socket, then a fresh `ps` re-check before any signal; no process-name
or port sweep.

| Arm | Phase | n | min ms | median ms | max ms |
|---|---|---:|---:|---:|---:|
| baseline `ec5dbac9` | cold | 10 | 1667.3 | 1744.1 | 1812.9 |
| baseline `ec5dbac9` | warm | 10 | 1483.8 | 1503.9 | 1517.1 |
| candidate `5ecf1f23` | cold | 10 | 1648.7 | 1706.1 | 2242.8 |
| candidate `5ecf1f23` | warm | 10 | 1415.9 | 1451.6 | 1605.5 |
| baseline `ec5dbac9`, second round | cold | 5 | 1698.6 | 1715.2 | 1749.9 |
| baseline `ec5dbac9`, second round | warm | 5 | 1430.8 | 1491.2 | 1517.5 |

Every successful sample reported `observation.state = observable` with source `lsof`; failed or
unobservable runs were not counted. The candidate cold median sits inside the baseline's own
round-to-round spread, and the warm median is about 2.7% below the second baseline round; neither is
a controlled speedup claim. The 2242.8 ms candidate cold sample is a host-noise outlier and is kept
in the table rather than discarded. No `runtime service run` process survived the phase.

### Fault injection

- A fake `lsof` earlier in `PATH` that exits 1 with no output: `up --wait --json` exits 1 with
  `code: observation_blind`; `status --json` reports `observation.state: blind` and `ports: []`, not
  "nothing is listening".
- `TestSecondServeRefusesAndStaleSocketIsCleaned`: a SIGKILLed runtime service leaves a stale socket, the
  second `runtime service run` refuses, and the socket is cleaned. PASS.
- `TestRuntimeServiceStopRestartKeepsServiceEvidence` and `TestSIGTERMEscalatesAndCleansServiceTree`: PASS.
- `TestCrashRecoveryKeepsWholeCommittedBatches`: the child writer is SIGKILLed mid-transaction, the
  database reopens cleanly, every visible batch is whole, and the sentinel survives. PASS.

**Not run for this commit**: native Linux or Windows execution; Docker/Colima was not running and no
real Docker daemon was used. Cross builds and vet are not native evidence. Windows x64 native and
Linux runner results exist for PR #270's head `8432e497`, whose tree was merged as `589b17a5`, not
for `5ecf1f23`; this follow-up needs its own CI run before those rows can be claimed.

### Spawn working-tree diff disposition (T0)

The local `spawn` change was a working-tree diff, never a commit. Against baseline `4fc663b` it
deleted two lines in `engine/internal/spawn/spawn.go`: the exported `Handle.Detached bool` field and
its `Detached: req.Detach` assignment. The pre-image blob is
`b17549f2f63fd1dc1ad2a99658f6661577cb986e`; the post-image blob recorded live was
`4d999315e53b7c844425074eae1582ee4c43c1d4`, which is not in the object database.

```diff
@@ -94,7 +94,6 @@ type Handle struct {
 	// Launch facts.
 	StartedAt time.Time
-	Detached  bool
 	Session   state.Session
@@ -134,7 +133,6 @@ func Spawn(ctx context.Context, req Request) (*Handle, error) {
 		Cwd:       cwd,
 		PortHint:  req.PortHint,
-		Detached:  req.Detach,
 		Session:   req.Session,
```

`Request` is unchanged, including `Request.Detach`; `Spawn(ctx context.Context, req Request)
(*Handle, error)` is unchanged. `Handle.Detached` was written once and never read, and has no JSON,
RPC or manifest surface. The last live observation was 2026-11-09T06:39Z (`M
engine/internal/spawn/spawn.go`, blob `4d999315`); `main` at `589b17a5` is clean and still has the
field. The D1 receive bundle that recorded the surrounding working tree is no longer on this machine,
and no reset, stash or clean of this diff appears in any recorded command.

**Maintainer decision (2026-11-11): discard.** The diff is not re-applied: it removes a write-only
field, so it changes no behavior, no signature and no external surface, while touching `spawn` would
violate the T0 freeze and invalidate the measured binary. No patch is stored: the two deleted lines
are reproduced above and the pre-image remains in history. The S2 gate sentence is "`Request` and
`Spawn` signatures unchanged; this diff is not included". The diff was only ever built and vetted in
the 2026-11-06 transcript; no product test ran with it.

## Client verification

The macOS client's verification scope is not here: it lives with the client code in
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos).
This document describes only the engine's verification scope and platform boundaries; the two sides
share one fact chain, but each side's checks prove only its own thing.
