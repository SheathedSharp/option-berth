# Verification scope

This page selects checks and defines what they establish. It is not a pass report. Historical runs are in [dated records](history/validation-through-20261114.md); a result only applies to its recorded revision, input, platform and environment.

For exact per-target promises, edge cases and platform limits, use the [detailed verification matrix](validation-reference.md). The task-oriented guide below does not replace or reduce that contract.

## Choose a check by the change

| Change | Evidence to collect | What it does not prove |
| --- | --- | --- |
| Human documentation | Bilingual structure, local files/anchors, actual CLI help, schema examples, skill anchors | Accuracy of prose or real service operation |
| Third-party source archives | Input locks, archive SHA256, Go h1 where available, full file inventory, preserved license texts | Legal originality or execution safety of downloaded code |
| Pure policy | Focused deterministic tests and race checks | OS adapters or lifecycle behavior |
| RPC/CLI/schema | Production rendering/adapter tests, generated-schema comparison, JSON/stdout and exit contracts | A complete healthy system scan |
| Store/recovery | Isolated production Store handles, failure atomicity, ownership and cancellation tests | User-data migration without the actual fixture |
| Lifecycle/observation | Isolated native integration/scenario tests on the intended OS | Another OS or a sandbox-denied scan |
| Performance | Same workload, source, cache conditions and repeated comparable measurements | Product speedup from a fake source or compilation |
| Release | The exact required checks and native acceptance in [releasing.md](releasing.md) | Approval merely because a branch builds |

## Local safety comes first

Read the package's TestMain and helpers before executing tests. Several packages use `internal/testenv`, and lifecycle suites start processes or send signals. An isolated `HOME` alone is not enough if cleanup can find unrelated processes. Use explicit private state, temporary projects and retained handles; do not operate on the everyday host, ledger or installed CLI. If side effects are not authorized, compile the affected test binary without running it and record that boundary.

Do not bypass denied `/bin/ps`, listener inspection or binding restrictions. A `scan_error`, partial snapshot or successful subscription reconnect is not evidence of a complete scan or real state-change cursor replay. Native macOS/Linux service behavior and native Windows evidence are distinct.

## Contracts and source-only checks

```sh
python3 scripts/check_docs.py
python3 scripts/check_doc_links.py
python3 scripts/check_doc_examples.py
python3 scripts/check_third_party.py
python3 scripts/check_skill_contract.py --skill-dir ../option-berth-skill
```

These checks do not start product services or use model keys. Verify the selected skill checkout is the intended version. Generated protocol and manifest schemas come from `engine/cmd/gen-schema`; generate to temporary output paths and compare both files. Do not edit schema JSON to make the comparison green.

Production-only `go build ./...` and `go vet ./...` from `engine/` do not execute TestMain. They still depend on the selected toolchain, module graph and target OS; record those. Downloaded third-party source is archived as data outside the Go package scan, never built or tested as part of archive verification.

## Storage ownership and naming

`scripts/check_runtime_naming.py` audits active identifiers with explicit migration/rejection, historical SQL, third-party Docker and standard-library exceptions. `check_runtime_naming_cli.py` checks a supplied candidate's real help, usage rejection, JSON and doctor IDs without starting a host.

`check_storage_ownership.py --legacy-engine <released-v2.4.0-engine>` uses pinned, unmodified old Store code and the candidate Store in private databases. `check_runtime_availability.py --binary <candidate> --legacy-engine <released-engine>` additionally starts only a private candidate under macOS sandbox-exec, denies user-tree access/IP outbound/other-process inspection and stops normally over RPC. Run it only with explicit host-test authorization. Its narrow handshake, exclusion and storage-read results are not whole-system acceptance.

## Models, telemetry and benchmarks

Pure attention tests use fake providers; real provider acceptance can send data and incur charges. Obtain consent separately, preserve task/option authorization and never put keys in a report. [Attention](../attention.md) is the user-facing entry; [adapter reference](attn-adapter.md) owns implementation details.

The [agent benchmark](agent-eval.md) defines scenario arms, repeated runs, intervals and cleanup ownership. A failed preflight, absent provider, cancelled run or incomplete artifact must remain visible. Never turn historical medians into a guarantee or remove a gate because the environment cannot run it.

## Record an honest result

State the revision and dirty state, exact command, target, fixture, exit and remaining limits. Distinguish compile-only, pure test, synthetic adapter, real isolated host, native service scenario and real user acceptance. Self-review is not another maintainer's independent approval. Release requirements remain authoritative even when a subset here passes.
