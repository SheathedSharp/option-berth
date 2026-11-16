# Troubleshooting

English | [中文](troubleshooting.zh.md)

## Keep the evidence first

Run `oberth doctor`, then `oberth status --json` in the affected worktree. Keep the error code, selected worktree and relevant service log. Before sharing output, remove credentials, environment values and private paths. `oberth feedback report` can prepare feedback; review its collection and submission behavior in the [reference](cli.md#oberth-feedback).

## Common symptoms

| Symptom | Check | Safe next step |
| --- | --- | --- |
| Background service is unreachable | Host status and its own log | Follow [background service](runtime-service.md); absence is not proof of a crash |
| Observation is blind or partial | `doctor --only listener_observation` in the failing environment | Retry from an ordinary terminal; unknown does not mean the port is free |
| Fixed port is occupied | Service error and `last_exit.port_holder`, when present | Identify the owner; do not kill an unrelated process or silently edit the port |
| `up --wait` times out | Service log, dependencies and health check | Fix the actual readiness failure; a live PID is not enough |
| A worker has no listening port | Its run and exit fields | No-port workers are supported; do not infer “stopped” from no listener |
| `runtime_service_busy` | Matching host and database owner | Read through the owner, or stop a previous host normally before a version switch |
| A group or identity is ambiguous | `worktrees --json` and the exact checkout path | Select the worktree/identity, not every matching name |
| An agent starts services with `nohup` or kills them by hand | `oberth doctor --only agent_integration` and the worktree's `AGENTS.md` / `CLAUDE.md` | Paste the section from the [integration guide](integrations.md); do not add a hook or let the agent keep bypassing oberth |
| No update is announced | Installed PATH/version and `version --check` | An automatic notice is not guaranteed on every invocation; explicit checking does not install |

## Report only what you can establish

Do not run `doctor --fix`, replace binaries, reset state or restart a user's services just to gather a report. A failed observation is not an empty system. Compilation and synthetic tests are not full platform acceptance; [verification scope](agent/validation.md) explains the distinction.
