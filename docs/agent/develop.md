# Development

Use this page to choose an entry point before changing code. Product behavior belongs in the [product guide](../product.md) and [CLI reference](../cli.md); implementation boundaries belong in [architecture](architecture.md). Read the root [AGENTS.md](../../AGENTS.md) before running commands.

## Prerequisites and repository layout

Use the Go version declared in `engine/go.mod`, Python 3 for repository scripts, and Git. Mage is optional task automation, not the product CLI. Website browser checks additionally require the tools in `site/package-lock.json`; they are not prerequisites for building the engine.

| Change | Start here | Keep in agreement |
|---|---|---|
| CLI command or JSON result | `engine/internal/cmd/`, `engine/internal/runtimesvc/rpc/` | CLI reference, generated schema, coding-agent skill |
| Runtime observation or operation | `engine/internal/runtimecore/` | Ownership, cancellation, run generation and partial-result contracts |
| Background host, sessions or subscriptions | `engine/internal/runtimesvc/` | Protocol, resource cleanup and database ownership |
| Manifest parsing | `engine/internal/groups/` | Manifest schema, examples and integrations |
| Persistence | `engine/internal/store/` | Schema compatibility, transactions and read-only paths |
| Optional attention adapter | `engine/internal/attn/` | Explicit consent and [adapter contract](attn-adapter.md) |
| User documentation | `docs/` | Both languages and [documentation rules](../AGENTS.md) |
| Source archives and attribution | `third_party/` | [Source inventory](../../third_party/README.md) and notices |

The [macOS client](https://github.com/SheathedSharp/option-berth-macos) and [coding-agent skill](https://github.com/SheathedSharp/option-berth-skill) have separate repositories. The skill contract check expects a sibling `option-berth-skill` checkout. The brand exporter reads the sibling client's geometry source; do not copy either product into this tree to satisfy a check.

## Build without installing

From `engine/`, these commands compile or inspect code without starting the product:

```sh
go build ./...
go vet ./...
go build -o ../bin/oberth ./cmd/oberth
```

`mage buildEngine` is the root-level build wrapper. `mage schema` regenerates the protocol and manifest JSON Schema; review the source and generated diff together. Building is not proof that scanning, starting services or upgrading a running installation works.

`mage install` changes the installation and can change PATH. Runtime-service commands and project `up`/`down` control processes. None belongs in a routine documentation or source-inspection check. For an explicitly authorized isolated run, use the [runtime-service guide](../runtime-service.md) and the safety boundaries in the root instructions.

Dependency versions remain in `engine/go.mod` and `engine/go.sum`. The [third-party source archives](../third-party.md) do not override module resolution. Resolve network or proxy problems deliberately; do not turn off checksum verification to make a build pass.

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
