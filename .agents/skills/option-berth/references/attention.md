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
read. The session owns its poller and is stopped when the task ends.

**Nothing on the machine creates the task context — you do.** It is your own
statement of what this task is and which of the fixed options it may present.
For a short task, pass it inline:

```sh
jev-attention --session --project "$PWD" \
  --task "keep the API healthy while the integration test runs" \
  --authorize inspect_logs,inspect_port,restart_service,continue_without_action,ask_user_for_context
```

For the complete fixed vocabulary, write a file and pass it. Choose a path
unique to this task; two parallel agents sharing one path overwrite each other.

```sh
cat > "${TMPDIR:-/tmp}/oberth-task-$$.json" <<'JSON'
{
  "task": "keep the API and worker healthy while running the integration test",
  "authorized_options": [
    "inspect_logs", "inspect_port", "inspect_dependency", "inspect_manifest",
    "restart_service", "start_service", "stop_service", "adopt_manifest",
    "continue_without_action", "ask_user_for_context"
  ]
}
JSON
jev-attention --session --project "$PWD" --task-context "${TMPDIR:-/tmp}/oberth-task-$$.json"
```

`--task` and `--authorize` override the same fields from the file. With neither
a task nor an allow-list, the runner stops with `task context requires
--task-context or --task` or `task context requires authorized_options or
--authorize` — it will not guess an allow-list for you.

`authorized_options` is an allow-list for the current task, not user approval;
side effects still require an explicit user choice. `--session` is an alias for
the long-lived `--subscribe` mode. The runner reads `$BERTH_HOME/jev.json`
(default `~/.option-berth/jev.json`); a copy there with `enabled: false` keeps
the session fact-only and prevents a remote call. Under an isolated `BERTH_HOME`
the client's `~/.option-berth/jev.json` is not the file being read.

For a task that remains active while services run, use the agent-side
subscription instead of rebuilding the request repeatedly:

```sh
jev-attention --subscribe --project "$PWD" --interval 2s \
  --task-context "${TMPDIR:-/tmp}/oberth-task-$$.json"
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
