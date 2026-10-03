# Cross-worktree observation

Use this guide when the task explicitly names another local checkout,
repository, or integration workspace. The agent owns the subscription; the
person does not need to start a watcher for the agent.

## Select and subscribe

Only use relationships stated by the task or the person. Never infer a member
from a port, process name, branch, or model guess. Selectors are additive.
The following names and paths are synthetic placeholders; replace them only
with the worktrees explicitly authorized for the task:

```sh
oberth events --workspace example-integration --worktree /path/to/example-api
oberth events --workspace example-integration --worktree /path/to/example-worker
oberth events --worktree /path/to/example-api --worktree /path/to/example-worker
oberth events --repository <repo-id>
```

Each participating agent supplies its own worktree or repository selector and
the same workspace label. If the task gives no label, derive a stable label
from the explicitly selected roots; the label coordinates members but does not
discover them. Keep the streaming process/session owned by the task and stop it
when the relation is no longer needed.

The first NDJSON record is a redacted `state.snapshot`; later records are
`state.changed` summaries. A worktree selector follows the daemon's existing
repository relation and includes sibling worktrees. A workspace is an
ephemeral daemon relation: the first live member must include a worktree or
repository, and later members may join by workspace name. `--once` reads a
snapshot without registering membership.

If the daemon lacks `state.scope`, restart it with `oberth daemon restart`, then
refresh the current worktree status before taking any action. If the stream
ends, emits `resync_required`, or reports `workspace_changed`, read a fresh
snapshot with `oberth events --once` before interpreting later changes.

A change can report a failure kind such as `service_failed`. On one, refresh the
current worktree status; if it publishes `attention_path`, read
[attention.md](attention.md) and follow its handoff before acting. A task that
stays active while services run should arm a task-scoped
`jev-attention --session` runner for each explicitly selected worktree. The
runner watches the same handoffs and records local quality telemetry without
starting, stopping, or restarting a service.

The stream contains runtime facts only: no commands, working directories,
logs, or environment values. It never starts, stops, or restarts a service.
