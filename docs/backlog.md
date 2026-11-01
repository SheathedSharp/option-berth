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
| Medium | End-to-end verification of orchestrator recipes | The Conductor / Cursor / Claude Code / Codex configurations in [integrations.md](integrations.md) come from each tool's official documentation and have not been run end to end in the real tools for creation, start and archive/cleanup. Completion condition: run setup, `status --json` and cleanup once each in an isolated `HOME` and a throwaway repository (including the path where the checkout was already deleted), recording the actual inputs and results. |

## Fixes within the supported scope

Within the supported scope, a reproducible wrong stop, attribution error or ledger corruption must be
fixed and cannot be papered over as "a later iteration". Verification commands and actual coverage are
in [validation.md](agent/validation.md); client behavior is in
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos).
