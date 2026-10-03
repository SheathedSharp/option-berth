# Attention and Jev handoff

Use this guide only when `oberth status --json` publishes `attention_path`, or
when a task explicitly asks about Jev, human-in-the-loop handling, or quality
feedback. The complete adapter contract is in
[`docs/jev-adapter.md`](https://github.com/SheathedSharp/option-berth/blob/main/docs/jev-adapter.md).

## Verify before acting

Read the exact path from the current status; do not scan `BERTH_HOME`. Refresh
status before acting and verify that the artifact's worktree, state revision,
and freshness marker still match. A missing, malformed, stale, or mismatched
artifact is not actionable.

`event` and `evidence` are code-produced facts. `assessment` is an optional Jev
recommendation. It is never a fact, diagnosis, command, permission, or new
option. Map recommendations only to the fixed option IDs present in the
artifact's `options` and preserve the existing human approval boundary for
`up`, `restart`, `down`, and manifest adoption.

If an option is needed, present one short choice with the event, evidence, and
bounded next steps. After the person chooses, run only the documented command,
report its result, and read status again. A low-confidence, stale, conflicting,
or context-only handoff goes back to the person.

## Task-scoped runner

When a task keeps services running or coordinates concurrent worktrees, the
agent should arm one task-scoped session immediately after the initial status
read. The session owns its poller and is stopped when the task ends:

```sh
jev-attention --session --project "$PWD" \
  --task-context /tmp/oberth-task-context.json
```

The task context should normally include the complete fixed vocabulary so the
runner can present the event's bounded candidates without making a second
authorization decision:

```json
{
  "task": "keep the API and worker healthy while running the integration test",
  "authorized_options": [
    "inspect_logs", "inspect_port", "inspect_dependency", "inspect_manifest",
    "restart_service", "start_service", "stop_service", "adopt_manifest",
    "continue_without_action", "ask_user_for_context"
  ]
}
```

`authorized_options` is an allow-list for the current task, not user approval;
side effects still require an explicit user choice. `--session` is an alias for
the long-lived `--subscribe` mode. A local `jev.json` with `enabled: false`
keeps the session fact-only and prevents a remote call.

For a task that remains active while services run, use the agent-side
subscription instead of rebuilding the request repeatedly:

```sh
jev-attention --subscribe --project "$PWD" --interval 2s \
  --task-context /tmp/oberth-task-context.json
```

The runner polls the existing CLI contract. It does not add a daemon watcher,
hook, MCP server, model call to the core engine, or an automatic action path.
It calls Jev only for a new event/revision or an uncached assessment; normal
polls are recorded as no-event observations.

## Quality feedback

The runner may write its redacted ledger to
`$BERTH_HOME/quality/events.ndjson`. Keep it local and do not copy it into the
repository or send it to Jev. When recording feedback, include only the event
identity, revision, fixed enums, and outcome; never include task text, logs,
commands, absolute paths, credentials, or free-form model output.

Use `jev-attention --quality-report` to inspect local results. A `null` metric
means there are not enough labeled cases. Read `labeled_cases`,
`unlabeled_cases`, and `label_coverage` before interpreting a score. The
`sessions` object explains whether a quiet ledger came from an armed session
with no events or from no session being started at all.

The runner's fixed vocabulary, preflight rules, settings, and exact feedback
schema are maintained in `docs/jev-adapter.md`; read that document before
changing or evaluating those interfaces.
