# Agent-facing JSON fields

English | [中文](json.zh.md)

The fields below are the minimal set an agent needs to make decisions from the `--json` output of
`status`, `up` and `restart`. The full meaning of each field is in its command's description; this
table is also the anchor source for `scripts/check_skill_contract.py`, so update the skill when adding
or renaming one.

| Field | Command | Purpose |
|---|---|---|
| `last_exit.reason` | `status` | The current failure reason; branch on it to logs, dependencies or port occupation. |
| `last_exit.port_hint` | `status` | The port this start was told to bind. |
| `last_exit.port_holder` | `status` | The process still holding the failing port at read time; absent means not observed, not that the port is free. |
| `exits[].hint` | `status` | The next step in a historical exit record, decided by whether a log file exists. |
| `services[].ready` | `up` / `restart` | The CLI's readiness annotation; `false` means unconfirmed, not failed. |
| `services[].port_holder` | `up` / `status --brief` | Holder evidence, same shape as in `status`, when the declared port conflicts or when a running service's listener is not its own run. |
| `health_status` | `status` | The result and reason of the declared health check. |
| `manifest_runtime_mismatch` | `status` | Whether the running instance comes from an old manifest. |
| `worktree.identity` | `status` | The expanded value of `${worktree}`, used to name per-worktree data. |
| `observation.state` | `status` | Whether the listener table can be trusted; read it before `ports`. |
| `observation.source` | `status` | The non-primary collector or `runtime_registry` fallback; non-empty means a missing port is unknown, not free. |
| `observation.sources` | `status` | Every collector a merged table came from, in chain order; present only for a merged read, and like `source` it means a missing port is unknown, not free. |
| `observation.degraded_since` | `status` | When the current degradation began; empty while the primary collector reads the whole table. |
| `observation.recovered_at` | `status` | When the current healthy run began after a degradation; empty when nothing was degraded. |
| `machine[].listening_known` | `status` | Whether `listening` was observed; false means the table cannot see that port's owner. |
| `attn_path` | `status` | The derived handoff file for the current abnormality; absent when there is none. |
| `attn` | `status` | Whether that handoff can be assessed now: `enabled`, fixed `reason`, `assessment` (`assessed`, `pending` or `unavailable`). |
| `services[].starting` | `status` | Instantaneous facts of an in-progress start; not a run and not the same as `running`. |
| `starting.phase` | `status` | `waiting_dependency`, `prepare` or `spawning`. |
| `starting.waiting_on` | `status` | Dependencies still being waited on; keeps the last known set when observation fails. |
| `starting.elapsed_ms` | `status` | Milliseconds elapsed in this start, computed at read time. |
| `starting.ready_timeout` | `status` | The readiness budget in effect for this start. |
| `starting.last_prepare_ms` | `status` | Duration of the previous prepare; null when unknown. |
| `services[].stats.*` | `status` | Service resource samples; an absent field means unknown, not 0. |
| `worktree.resources.*` | `status` | The worktree aggregate of available samples; absent when there are none. |
| `manifest_hash` | `status` | The manifest fingerprint at process start. |
| `runtime_spec_hash` | `status` | Fingerprint of the current runtime parameters; differing from `manifest_hash` means it comes from an old manifest. |
| `code_changed_since_start` | `status` | Whether the service is running the current source; absent means unknown. |
| `code_changed_files` | `status` | The number of changed files that can be compared exactly. |
| `code_hint` | `status` | The deterministic next step once the code has changed. |
| `code_head` | `status` | The commit at start; absent when not a Git checkout or when there is no commit. |
| `services[].exit_reason` | `status --brief` | The failure reason in the brief view. |
| `services[].exit_code` | `status --brief` | The exit code in the brief view. |
