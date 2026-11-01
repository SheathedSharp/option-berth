# Current capability and open work

English | [中文](backlog.zh.md)

The [product definition](product.md) and [support boundaries](agent/architecture.md) are authoritative.
The through-line is what a coding agent can do. Presentation-layer and skill progress are not in this
repository: the macOS client is
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) and the agent skill is
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill).

## Upcoming capability

| Priority | Capability | Current gap and completion condition |
| --- | --- | --- |
| High | Whether the agent-facing capability surface is sufficient | The through-line is what a coding agent can do, so "can an agent take this code, run it itself, and judge for itself why it did not run" outranks any interface. **One round ran on 2026-10-28**: real Claude Code, on an unfamiliar worktree, using only the CLI and the skill, completed "start the service, fail, read the log, fix, start again" — the core loop in 6 steps (inputs and results in [validation.md](agent/validation.md) "Core acceptance"). The two places that round got stuck are fixed: a failure hint no longer points at a log that does not exist, and an unlistened `machine:` dependency no longer publishes a handoff (contract in [cli.md](cli.md)). **From 2026-10-30** that acceptance round was distilled into a fixed scenario library (missing artifact, port occupied, dependency not ready, manifest/process mismatch, same-repository dual worktree), see [agent-eval.md](agent/agent-eval.md). **From 2026-10-31** a ten-service large-project tier was added (slow dependency, generated artifact, migration, portless worker, three worktrees) plus `oberth` / `native` comparison arms, with the report outputting completion rate, steps, elapsed time, output bytes, process probing, wrong stops and cross-worktree differences; the fixture shares the public example [stack-workshop](../examples/stack-workshop/README.md). Still missing: a comparable release matrix from real Codex / OpenCode / Pi on an ordinary terminal that can observe listeners or in CI, plus one manual fixture tier with a real JVM / Node build / compose Postgres+Redis; preflight inside this machine's agent sandbox refuses to produce a table. |
| High | The evidence base for a release claim | Only half of #1 has been measured. The last real matrix (2026-11-01, CI run 41280627281, `main` at `81bf405`) covered **one** provider (Claude Code), the eight `large` scenarios, both arms and **repeat 2**; every row was comparable, but the table is a median over two runs with no spread, and nothing at all covers `core`, the four-provider claim, or `n >= 5`. The 2026-10-28 core acceptance is a single manual round. Completion condition: a matrix of `4 providers × (core + large) × n ≥ 5` with intervals in the report, plus the manual JVM / Node / compose tier above; until then no capability claim outranks the rows that actually exist. Two prerequisites are open work, not this row: the suite and arm generalization (a `cold` suite, an `oberth-noskill` arm, `--repeat` accepted as a range) does not exist yet, and `docs/agent/agent-eval.md` documents only `oberth` / `native`. |
| Medium | Cold-start baseline (a repository with no manifest) | Every scenario library row starts from a hand-written `oberth.yaml`; the step before it — an unfamiliar repository with no manifest at all, through draft → adopt → `up --wait` to a healthy service — is unmeasured, so there is no baseline against which a drafting improvement could be judged. Completion condition: a fixture suite with three repository shapes (compose + Makefile, Node monorepo, Maven multi-module) driven only by the Python standard library, reporting success rate, steps, elapsed time, how many times the manifest was edited after the first failure, undeclared and over-declared service counts, and the safety check that no tracked file other than `oberth.yaml` changed. `init draft` starts a second agent process, so each cold run costs one more model call and the budget belongs in the scenario. Do not change drafting (`internal/draft/brief.go`) before that baseline exists. |
| Medium | End-to-end verification of orchestrator recipes | The Conductor / Cursor / Claude Code / Codex configurations in [integrations.md](integrations.md) come from each tool's official documentation and have not been run end to end in the real tools for creation, start and archive/cleanup. Completion condition: run setup, `status --json` and cleanup once each in an isolated `HOME` and a throwaway repository (including the path where the checkout was already deleted), recording the actual inputs and results. |

## Rules while the list above is open

These are constraints on how work gets accepted, not capabilities. They exist because the open rows
above are claims about evidence, and a new surface makes the evidence question harder, not easier.

- **No new commands until the core is proven.** The product surface is limited to the command layers in
  [product.md](product.md) "Command layers". A capability that cannot be expressed inside an existing
  layer waits for the rows above rather than adding a layer.
- **The macOS 27 listener limitation is fixed, and macOS 27 itself is still unverified in CI.** Issue
  #156 (a Go child process reading an empty `netstat` table) is closed: the collector falls back to
  `lsof` (contract in [cli.md](cli.md) "Listener observation", evidence in
  [validation.md](agent/validation.md)). Hosted runners offer `macos-14`, `macos-15` and `macos-26`
  images; there is no macOS 27 image, so the fix is proven on this machine only and CI's macOS rows do
  not cover it. Reproducing it locally is a documented manual step, not a CI result.

## Fixes within the supported scope

Within the supported scope, a reproducible wrong stop, attribution error or ledger corruption must be
fixed and cannot be papered over as "a later iteration". Verification commands and actual coverage are
in [validation.md](agent/validation.md); client behavior is in
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos).
