# Engineering rules

This file holds only the boundaries specific to this repository that you must know before touching it.
The general delivery rules (Chinese `type(scope): subject` commit messages, one topic per commit,
branching and merging, never changing a check threshold on your own) come from this machine's global
rules and are not repeated here.

## Delivery

Merging happens through a remote PR: push the topic branch to `origin`, wait for every required check
on that PR (the list is [`.github/quality-gate-checks.json`](.github/quality-gate-checks.json), with
`main`'s ruleset as the authority), and merge with a merge commit once the user confirms. Never merge a
topic branch into `main` locally and push it. The release script's atomic push is the exception.
When a change touches a workflow, CODEOWNERS, the race list or a release/check script, `Gate integrity`
requires the PR author to be a repository maintainer; do not work around it. Commands, failure handling
and cleanup are in the project skill
[`option-berth-delivery`](.agents/skills/option-berth-delivery/SKILL.md).

Contracts and practices each have a home; when they conflict, the contract wins:

| What you need | Where to go |
|---|---|
| Product definition, whether to build something at all | [docs/product.md](docs/product.md) |
| CLI / RPC / JSON / manifest fields | [docs/cli.md](docs/cli.md) |
| Work records and handoff | [docs/workplace.md](docs/workplace.md) |
| Production implementation boundaries, the chain of facts | [docs/agent/architecture.md](docs/agent/architecture.md) |
| Integrating orchestrators, writing an AGENTS.md recipe | [docs/integrations.md](docs/integrations.md) |
| Build, run, and what to run after which change | [docs/agent/develop.md](docs/agent/develop.md) |
| What each check proves and its platform scope | [docs/agent/validation.md](docs/agent/validation.md) |
| Versions, tags, pre-release gates | [docs/agent/releasing.md](docs/agent/releasing.md) |
| Agent scenario library and release matrix | [docs/agent/agent-eval.md](docs/agent/agent-eval.md) |
| Optional attention adapter | [docs/agent/jev-adapter.md](docs/agent/jev-adapter.md) |
| Changing the macOS client | [option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) (separate product) |
| Changing the coding-agent skill | [option-berth-skill](https://github.com/SheathedSharp/option-berth-skill) (separate product) |

Past designs, temporary debugging and requirements from older iterations are not the current contract;
do not build implementations to match them.

## One chain of facts

`worktree → manifest → service → run → runtime facts` is the only source of runtime truth.
Attach new capabilities to this chain; never stand up a second source of state, and never cache a second
copy of runtime results to route around a failure path.

- A person confirms the manifest; a model only drafts it. `machine:` dependencies are read-only, and the
  product CLI's Git operations are read-only.
- Cancellation, unknown and observation failure are not success; a signal receipt does not mean the
  process exited; results from an older generation cannot overwrite a newer snapshot.
- Locks, caches and queues must state their ownership and release boundaries; do not fake a hard timeout
  with a detached task.

Details in [docs/agent/architecture.md](docs/agent/architecture.md).

## Interface

The `oberth` CLI, RPC, JSON and manifest are this project's external interface: a change must update the
schema, the documentation and the regression together. JSON stdout never mixes in diagnostics. Mage is
only a development tool; its targets and logs are not part of product semantics.

## Validation

Run what you changed; entry points are in [docs/agent/develop.md](docs/agent/develop.md), and what each
check proves is in [docs/agent/validation.md](docs/agent/validation.md).

- Full service scenarios are verified only on macOS/Linux; Windows evidence is a separate native
  build/smoke plus an arm64 cross-build.
- GUI changes must distinguish frozen rendering from real interaction: an offscreen image does not prove
  the mouse, input methods or long-running load work.
- Performance conclusions need comparable measurements; numbers from different baselines are not
  conclusions.
- Tests, real services and screenshots use an isolated `HOME`/`BERTH_HOME` and throwaway projects, and
  you reclaim the processes you started afterwards. Never touch the user's everyday daemon, ledger or
  real repository writes; installing, releasing, Git writes and repository-visibility changes require
  explicit authorization.

Report only inputs and results you actually executed. Formatting checks, regression definitions and a
green light on an older commit do not substitute for a test; state plainly any validation you did not
run.

## Assets

Examples, fixtures and screenshots use synthetic identities and paths. Never commit credentials, private
logs, real-user screenshots, caches, raw scan reports or historical backups. Keep the root MIT license,
the upstream copyright and the bundled fonts' OFL: do not modify system fonts and do not relicense the
fonts as MIT.

## Documentation

Keep only the contracts, evidence and real gaps that are useful now; once a landed plan is merged into
the contract documents, delete it from the plan.

Human-facing documents under `docs/` are bilingual pairs: `foo.md` (English, authoritative) and
`foo.zh.md`. Both sides carry equal authority and must say the same thing; a change to one updates the
other in the same PR. `scripts/check_docs.py` enforces pair completeness, matching heading structure,
byte-identical code blocks and the language switcher.

Documents under `docs/agent/` are read by coding agents and are English only. They are process and
engineering material, not the contract. A fact that belongs to the CLI, RPC, JSON or manifest contract
has exactly one home in `docs/`; never copy it into `docs/agent/`.

The documentation standard — where a fact belongs, what each tier must not contain — is
[docs/AGENTS.md](docs/AGENTS.md).
