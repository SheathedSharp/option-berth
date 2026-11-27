# Development

Use this page to choose an entry point before changing code. Product behavior belongs in the [product guide](../product.md) and [CLI reference](../cli.md); implementation boundaries belong in [architecture](architecture.md). Read the root [AGENTS.md](../../AGENTS.md) before running commands.

## Prerequisites and repository layout

Use the Go version declared in `engine/go.mod`, Python 3 for repository scripts, and Git. Mage is optional task automation, not the product CLI. Website browser checks additionally require the tools in `site/package-lock.json`; they are not prerequisites for building the engine.

| Change | Start here | Keep in agreement |
|---|---|---|
| CLI command or JSON result | `engine/internal/cmd/`, `engine/internal/runtimesvc/rpc/` | CLI reference, generated schema, coding-agent skill |
| Runtime observation or operation | `engine/internal/runtimecore/` | Ownership, cancellation, run generation, the `World` reduction and partial-result contracts |
| Background host, sessions or subscriptions | `engine/internal/runtimesvc/` | Protocol, resource cleanup and database ownership |
| Manifest parsing | `engine/internal/groups/` | Manifest schema, examples and integrations |
| Persistence | `engine/internal/store/` | Schema compatibility, transactions and read-only paths |
| Attention notes or optional triage | `engine/internal/attn/engine/` (deterministic), `engine/internal/attn/model/` (optional) | Explicit consent, the notes store and [adapter contract](attn-adapter.md) |
| User documentation | `docs/` | Both languages and [documentation rules](../AGENTS.md) |
| Source archives and attribution | `third_party/` | [Source inventory](../../third_party/README.md) and notices |

The [coding-agent skill](https://github.com/SheathedSharp/option-berth-skill) has a separate repository. The skill contract check expects a sibling `option-berth-skill` checkout; do not copy that product into this tree to satisfy a check.

## Build without installing

From `engine/`, these commands compile or inspect code without starting the product:

```sh
go build ./...
go vet ./...
go build -o ../bin/oberth ./cmd/oberth
```

`mage buildEngine` is the root-level build wrapper. `mage schema` regenerates the protocol and manifest JSON Schema; review the source and generated diff together. Building is not proof that scanning, starting services or upgrading a running installation works. CI takes the same steps through the `.github/actions/setup-engine` composite action and the site through `.github/actions/setup-site`, so a local build and a workflow build are separate evidence.

`mage install` changes the installation and can change PATH. Runtime-service commands and project `up`/`down` control processes. None belongs in a routine documentation or source-inspection check. For an explicitly authorized isolated run, use the [runtime-service guide](../runtime-service.md) and the safety boundaries in the root instructions.

Dependency versions remain in `engine/go.mod` and `engine/go.sum`. The [third-party source archives](../third-party.md) do not override module resolution. Resolve network or proxy problems deliberately; do not turn off checksum verification to make a build pass.

`scripts/retry.sh` retries the network fetch of a tool install only (`go install`, `apt-get update`, `apt-get install`). Never wrap a test, vet, build or verification step with it: the retry would turn a real failure into a slower red build instead of a result about the change.

A failed verification run gets a second attempt only through `.github/workflows/flake-retry.yml`, never through a wrapped step. It has to be a separate workflow: `gh run rerun` only accepts a completed run, so no job inside the run it would re-run can make that call. `flake-retry.yml` starts on `workflow_run` for `CI`, `Nightly full verification` and `Windows build verification` — reused readiness and the Git read race are jobs inside `CI`, so its run covers them — and only for a failure or for a success on a later attempt.

`scripts/ci_failures.py` classifies one attempt from the job and step names and the Go test output: `infrastructure` (a job that never ran a step, or a step that failed while installing a tool), `flaky-candidate` (a test or package listed in `scripts/flaky-tests.txt`, which lists nothing by default), or `deterministic` (everything else, including every shape the classifier does not recognise). Only the first two are re-run, with `gh run rerun --failed`, at most twice; `scripts/retry-ci.sh` decides the outcome. The record goes onto the pull request the run belongs to, and a failure that stands with no pull request is escalated through `scripts/reconcile_repo.py --failure`. A run that only went green on a later attempt is classified again and says so, so a flake is never silently green; any other conclusion, a cancellation included, is left alone. `Gate / required checks` keeps its meaning: it fails on the attempt that failed and turns green on the attempt the re-run produces.

A `workflow_run` trigger is registered from the default branch only, so `flake-retry.yml` starts working once it is merged into `main`, not on the pull request that adds it. Do not add a line to `scripts/flaky-tests.txt` without the run that proved this test can fail on unchanged code; a build failure is never a candidate.

## Choose checks by side effect

Read [validation.md](validation.md) first. A test name, a `-run` filter or a temporary `BERTH_HOME` does not make a package's `TestMain` safe. Review package setup and imported helpers before execution; use `go test -c` where only compilation is authorized.

For documentation and source data, the offline/static entry points are:

```sh
python3 scripts/check_docs.py
python3 scripts/check_skill_contract.py --skill-dir ../option-berth-skill
```

Python unit-test modules also have different behavior. Read their setup before selecting them; do not replace this decision with an unreviewed discovery command. Schema generation and pure parser tests can validate examples without starting the example services.

Full Go tests, race suites, real-service scenarios, Docker and model evaluations belong in their authorized isolated environment. Existing [workflows](../../.github/workflows) and [release gates](releasing.md) remain authoritative; this guide does not reduce their requirements. A local static check is not a substitute for those gates.

## Add or change a feature

Start from the owning contract, then follow the production call chain. Keep observed facts, service identity and group configuration distinct; the architecture guide describes their owners. Do not implement a parallel cache or result format just for a new adapter.

For a CLI change, test the helper or adapter actually called by production. Update JSON fields, error/exit behavior, generated schemas and the skill contract together. For a runtime-service change, include cancellation, reconnect, resource release and database ownership cases; a fake provider benchmark does not establish product speed.

For documentation changes, keep the short task guides separate from exhaustive reference. Preserve negative statements and authorization boundaries in both languages. Move old experiment logs to the marked [history directory](history/), not into current acceptance claims.

## Platform-specific acceptance

Listener observation depends on operating-system permissions. A sandbox refusal to inspect processes or ports is evidence of a restricted test environment, not proof of a product regression. Do not relax the sandbox or control unrelated processes to complete a test.

For macOS behavior not covered by the configured hosted runner, record the actual OS, revision, observation backend and isolated inputs in a separate acceptance report. Follow the real-service procedure only with explicit authorization. Do not label local results as CI results, and do not treat an older archived report as acceptance of today's revision.

## Commits and release

Use product-language commit subjects and an appropriate scope, not internal milestone codes or vague completion slogans. Before a commit, reread HEAD and status so another session's changes are not included accidentally.

Local development does not imply permission to install, push, merge or release. When authorized, use the normal PR checks and protected version/tag process in [releasing.md](releasing.md); never merge into the user's main worktree as a shortcut.
