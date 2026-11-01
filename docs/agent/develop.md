# Development

The engineering guide for this repository: how to build, how to run, and what to run after a change.
Contracts live in [product.md](../product.md), [cli.md](../cli.md) and [architecture.md](architecture.md);
the hard boundaries an agent must know up front are in the root [AGENTS.md](../../AGENTS.md), and this
page does not repeat them.

## How the repository splits

| Area | Path | Notes |
|------|------|-------|
| Engine and CLI | `engine/` | The `oberth` product interface; Go version in `engine/go.mod` |
| Development automation | `mage` targets at the repository root | Build and verification entry points; do **not** treat Mage as the product CLI |
| Documentation | `docs/`, `docs/agent/` | Human contracts in `docs/`, engineering process in `docs/agent/`; verification and release in [validation.md](validation.md) and [releasing.md](releasing.md) |

The interface and the agent guidance are not in this repository; they are independent products:
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) (the macOS client) and
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill) (the coding-agent skill).
All three meet only at the CLI and its `--json` contract.

Implementation boundaries and inviolable constraints are in the root [AGENTS.md](../../AGENTS.md);
this page covers only how to move these files.

## Build and run

From the repository root:

```sh
mage build          # engine + Jev adapter
mage buildEngine    # engine only → ./bin/oberth
mage runCLI "status --json"
mage runDaemon      # start a daemon with the CLI just built (isolated use)
mage stop
mage schema         # regenerate the protocol and manifest JSON Schema
```

`mage install` writes to the install directory and modifies PATH (skip with `NO_MODIFY_PATH=1`);
**do not** install to or release into the user's everyday environment without explicit authorization.

The client and the skill build and verify in their own repositories; see their READMEs and AGENTS.md.

## Verification (run what you changed)

Rules and boundaries are in [validation.md](validation.md). Common entry points (under `engine/`):

```sh
go build ./...
go vet ./...
go test -count=1 -timeout=8m ./...
```

PRs and pushes to `main` run these three on macOS/Linux automatically, along with the Mage and Python
helper tests, plus a native build and script parsing on Windows; the Linux job also cross-builds six
release targets with `CGO_ENABLED=0`. The full gates — core race tests, real service scenarios — still
run per [validation.md](validation.md) on the daily, manual or release paths. CI-configuration changes
also run actionlint and the required-checks consistency check; the list is in
`.github/quality-gate-checks.json`.

If `proxy.golang.org` times out fetching modules, prefix the same shell with
`GOPROXY=https://goproxy.cn` (or a mirror reachable from your environment) and rerun the commands.
Set only `GOPROXY`: Go still verifies checksums through the proxy's `/sumdb/` endpoint, so `GOSUMDB=off`
is unnecessary. Do not append `,direct` either — when the proxy misses, Go falls back to a direct GitHub
connection and stalls for a long time. A timeout means the network, not that the code fails to compile.

Scripts, release and brand generation:

```sh
python3 -m unittest discover -s scripts -p '*_test.py'
python3 brand/build-option-berth.py --check
python3 scripts/check_skill_contract.py --skill-dir ../option-berth-skill
python3 scripts/agent_eval.py --list          # core/large/cold scenarios and the comparison arms
python3 scripts/agent_eval.py --dry-run --suite large  # build the large fixture, launch no agent
python3 scripts/agent_eval.py --dry-run --suite cold   # cold-start fixtures: no manifest, no agent
```

`check_skill_contract.py` needs a sibling skill checkout; CI checks out `option-berth-skill` in the
Linux job before running it. Its anchors come from this repository's CLI contract (error codes, the
reference table and the agent-facing JSON field table), the manifest schema and the observation
constants. Do not edit skill content in the engine repository.

The real agent matrix needs an ordinary terminal (outside a coding-agent sandbox) or CI with
credentials. The manual CI entry point here is `.github/workflows/agent-eval.yml`, which hangs off an
`agent-eval` environment that requires reviewer approval; metrics, models and the gateway protocol
preflight are in [agent-eval.md](agent-eval.md).

Isolation and authorization boundaries are in [AGENTS.md](../../AGENTS.md) "Validation"; interface
compatibility and implementation constraints such as "cancellation and unknown are not success" are in
the same file's "Interface" and "One chain of facts" sections.

### Listener observation on macOS 27 has to be re-verified locally

GitHub-hosted runners top out at macOS 26 (`macos-26` / `macos-latest` is macOS 26 arm64) and this
repository's workflows still pin `macos-15`. The macOS 27 behavior — a `netstat` started by a parent
Apple has not signed gets no TCP table — therefore has **no CI coverage** and can only be re-verified
on a local macOS 27 machine. Do not present a local record as CI evidence. Steps (isolated
`HOME`/`BERTH_HOME`, a throwaway project, cleaned up afterwards):

```sh
export HOME=$(mktemp -d) BERTH_HOME=$(mktemp -d)
cd engine && go build -o /tmp/oberth-mac27 ./cmd/oberth
/tmp/oberth-mac27 doctor --only listener_observation     # expect observable, detail naming lsof
/tmp/oberth-mac27 serve --detach && /tmp/oberth-mac27 daemon status
cd ../examples/hello-worktree
/tmp/oberth-mac27 up --wait --json && /tmp/oberth-mac27 status --json && /tmp/oberth-mac27 down --json
cd ../../engine && go test -tags integration ./internal/scenario/... -count=1
/tmp/oberth-mac27 daemon stop    # do not leave a daemon behind
```

Record the result in [validation.md](validation.md) "Listener observation", with the commit, the
inputs and the actual output. Add a CI job once GitHub offers a macOS 27 image.

## Commits and collaboration

Additional requirements for this repository: subjects use product language, never this repository's
internal milestone codes (`M0`–`M3`, phase names) and never vague words like "closed loop" or "filled
in". `scope` matches the tree you touched (`engine`, `docs`, `ci`, `brand`, …).

- Before pushing, run `git log` / `git status` and re-read the target HEAD; watch for concurrent commits.
- Merging goes through a remote PR: push the branch to `origin`, open a PR, wait for
  `Engine verification` to pass, and merge with a merge commit once the user confirms; never merge into
  `main` locally. Releases follow the same version-PR plus annotated-tag path, verified by
  `Verified release` before publishing — see [releasing.md](releasing.md).
- The release and tagging flow is in [releasing.md](releasing.md); the version and tag rules are
  maintained only there.

## Related topics

- CLI and RPC contract: [cli.md](../cli.md); product boundaries: [product.md](../product.md);
  implementation and the chain of facts: [architecture.md](architecture.md)
- Optional attention adapter: [jev-adapter.md](jev-adapter.md)
- Integrating orchestrators and dev servers: [integrations.md](../integrations.md)
- Client: [option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)
- Coding-agent skill: [option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)
