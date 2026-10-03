# Jev attention adapter

`engine/cmd/jev-attention` is an optional, read-only adapter for the agent
attention handoff. Its `--auto` runner reads `oberth status --json --no-mark`,
validates the exact published artifact, asks TypeSafe Jev whether the event
affects the current development task and needs the user's choice, then stores
only the validated typed assessment in the same artifact. Its manual mode can
still assess an explicitly supplied artifact. It never reads service logs,
starts or stops a process, edits a manifest, or invents a command.

The adapter supports TypeSafe's endpoint and OpenRouter's Jev Decisions API.
For OpenRouter, use `POST https://openrouter.ai/api/alpha/decisions` with the
`typesafe/jev-1.13` model. Both endpoints accept the same structured `state`,
model, and typed `questions`; responses contain typed `answers`. The adapter
uses two `noul` questions (`task_relevant`, `needs_human`) and a `choice`
question for the authorized fixed options. See the [OpenRouter Jev guide](https://openrouter.ai/blog/insights/what-is-jev/)
and [TypeSafe API reference](https://docs.typesafe.ai/api/) for the upstream
wire contract.

## Run it

For a task that keeps services running, prefer the task-scoped session form:

```sh
jev-attention --session --project "$PWD" --task-context /tmp/oberth-task-context.json
```

`--session` is the lifecycle name for the long-lived `--subscribe` mode. The
agent starts it after the initial status read and stops it with the task. It
polls facts continuously but calls Jev only for a new event/revision or an
uncached assessment.

The agent-side runner performs the status read and exact artifact lookup in one
operation. Do not scan the `BERTH_HOME/attention` directory:

```sh
jev-attention --auto --project "$PWD" --task-context /tmp/oberth-task-context.json
```

Create a short task context outside the repository. The option list is an
explicit allow-list supplied by the development agent, not a model decision:

```json
{
  "task": "Fix the API startup failure so the integration test can run",
  "authorized_options": ["inspect_logs", "restart_service", "continue_without_action"]
}
```

The command prints one JSON value. `clear`, `continue`, `human_required`,
`unavailable`, and `stale` are the runner statuses. It refreshes status before
returning and includes a bounded `request` only for the current event. Successful
runner output also exposes the stable `worktree_id`, the opaque `context_id`
for this task/allow-list, and the local `telemetry_path` used for quality
observations. The agent must map a user's explicit choice to an existing documented CLI command.
The runner never executes the selected option.

For a task that remains active while services run, use the subscription mode:

```sh
jev-attention --subscribe --project "$PWD" --interval 2s \
  --task-context /tmp/oberth-task-context.json
```

It emits NDJSON: one `subscribed` record, then only changed event/revision/
assessment handoffs, and `stopped` when the agent cancels the process. It polls
`oberth status --json --no-mark`, so the daemon and core CLI remain model free
and no hook or MCP registration is needed.

When `OPENROUTER_API_KEY` is present, the adapter selects
`https://openrouter.ai/api/alpha/decisions` and `typesafe/jev-1.13`
automatically. `OPENROUTER_ENDPOINT` and `OPENROUTER_MODEL` can override
those defaults. `TYPESAFE_API_KEY`, `TYPESAFE_ENDPOINT`, and `TYPESAFE_MODEL`
remain available for the native TypeSafe endpoint. Never put either provider's
key in a repository file or a task-context JSON file.

The macOS client's **设置 → Jev 增强** page is the persistent setup path. It
writes an owner-only file at `~/.option-berth/jev.json` (or
`$BERTH_HOME/jev.json` in an isolated run):

```json
{
  "schema": "oberth.jev-config/v1",
  "enabled": true,
  "provider": "openrouter",
  "endpoint": "https://openrouter.ai/api/alpha/decisions",
  "model": "typesafe/jev-1.13",
  "api_key": "…",
  "timeout_ms": 10000
}
```

The adapter reads this file automatically, so an agent does not need to
re-export a shell variable for every task. An explicit `OPENROUTER_API_KEY` or
`TYPESAFE_API_KEY` still wins for CI and one-off isolation; `enabled: false`
suppresses the local key. A pre-settings `~/.option-berth/decide.key` is read
only when `jev.json` does not exist. The settings page may show that legacy key
as a masked migration candidate; the adapter itself never copies it. The GUI
masks the key and the adapter never puts it in attention artifacts, quality
telemetry, or daemon output.

For one-off use, `--task` and `--authorize inspect_logs,restart_service` can
replace `--task-context`. `--authorize` (or `authorized_options` in the JSON)
is required by `--auto` and `--subscribe`; artifact options are candidates,
not implicit authorization. A long-running agent may provide a broad subset of
the fixed option vocabulary; the runner validates every ID and sends only its
intersection with the current event's candidates. An empty intersection is an
error. `--endpoint`, `--model`, and `--timeout` override
the selected provider's endpoint/model and the ten-second default timeout.
For persistent local use, prefer the settings file; `--api-key` is provided
for test harnesses and can expose a secret in process listings.

## Failure and privacy behavior

Missing keys, timeouts, HTTP errors (including 401, 429, and 529), and malformed
responses produce `unavailable`; expired or changed artifacts produce `stale`.
Both leave the fact-only artifact unchanged.
The core `oberth` CLI remains usable and the agent can ask the user directly.
The adapter re-reads the artifact before writing and refuses to attach a result
if its event ID or state revision changed while the request was in flight.

Only a hashed worktree identity, a length-limited branch label, the event
kind/service/reason summaries (for all entries in `events`), state revision,
fixed evidence references, task summary, and fixed option IDs are sent to
the selected Jev provider. Task text is length-limited
and obvious credentials and local paths are redacted. Full logs, environment
variables, API keys, and unrelated worktrees are never sent or copied into the
artifact. Model output is accepted only when its option IDs and probabilities
match the fixed artifact options; free-form text cannot enter the handoff.

The runner does not claim calibrated Jev probabilities. Its fixed policy asks
the user for every side effect, incomplete or low-confidence result, close
option ranking, and relevant `continue_without_action`; only a clearly
separated, confident read-only inspection may return `continue`. A stale result
always requires a fresh status read.

## Local quality telemetry

The adapter keeps a local, owner-readable quality ledger at
`$BERTH_HOME/quality/events.ndjson`, or `~/.option-berth/quality/events.ndjson`
when `BERTH_HOME` is unset. `--auto` and `--subscribe` append a redacted
assessment observation after each new runner state. The record contains the
event/revision, fixed options, Jev typed values, the conservative HIL decision,
and timestamps; it does not contain task text, log contents, absolute paths,
commands, environment variables, or API keys. The file is mode `0600` and is
rotated at 8 MiB, with one `.1` backup.

The observation alone cannot tell whether a person was actually needed. After
the agent has shown the request and, if applicable, executed the person's
choice, it should write a short feedback document without putting the task or
logs into it:

```json
{
  "schema": "oberth.attention.feedback/v1",
  "event_id": "…",
  "state_revision": "…",
  "context_id": "…",
  "human_needed": true,
  "task_relevant": true,
  "user_decision": "selected",
  "chosen_option": "restart_service",
  "action_status": "succeeded",
  "resolved": true
}
```

Append it with:

```sh
jev-attention --feedback /tmp/oberth-attention-feedback.json --project "$PWD"
```

`human_needed` is the ground-truth label for the event: set it to `false` when
the runner asked for a person unnecessarily, and to `true` when the agent later
found that a choice was required even if the runner had returned `continue`.
`user_decision`, `chosen_option`, and `action_status` may be written in a later
feedback record for the same event/revision; the report merges the latest
values. Copy `event_id`, `attention_revision` (as `state_revision`),
`context_id`, and `worktree_id` from the latest runner record rather than from
an older status poll. Feedback never changes the attention artifact or grants
permission.

Inspect the accumulated evidence with:

```sh
jev-attention --quality-report
jev-attention --quality-report --since 168h --quality-project example-worker
jev-attention --quality-report --since 168h --quality-project example-api
```

The report includes session lifecycle and polling counts, observation/status counts, labelled/unlabelled-case
coverage, assessment availability, routing-reason and model/option breakdowns,
user decisions, the conservative human-routing precision/recall/F1, Jev's
`needs_human` and `task_relevant` Brier scores and mean absolute errors,
recommendation matches, action success rate, resolution rate, and
event-to-observation latency. A metric is `null` until enough feedback labels
exist; the synthetic policy tests do not populate these production metrics.
Ledger write failures are diagnostic only and do not change the runner result.

## Action preflight

When an agent is preparing a side effect that is not itself a runtime fact,
the same binary can create a separate preflight request from a freshly saved
`oberth status --json` response. This is an external adapter artifact under
`BERTH_HOME/attention/preflight/`; it never enters the daemon or replaces the
runtime attention artifact:

```sh
oberth status --json > /tmp/oberth-status.json
jev-attention --status /tmp/oberth-status.json \
  --action restart_service --service api \
  --permission 'show this choice to the user before restarting' \
  --task 'Get the API ready for the integration test'
```

The supported action intents are `start_service`, `restart_service`,
`stop_service`, and `adopt_manifest`. A start/restart target must name a
service present in that status snapshot. `stop_service` is deliberately
project-wide because the CLI has no service-scoped `down`; the request lists
all declared project services. `adopt_manifest` requires a `--draft-group`
that is present in the same snapshot, belongs to the current worktree, and
resolves to a regular file under `BERTH_HOME/drafts`; the request records its
SHA-256 so a changed draft becomes stale.

The command writes the bounded request before contacting Jev. Without a key or
when Jev is unavailable it returns `status: "unavailable"` while leaving the
preflight request available for the agent to present directly. With Jev, only
`needs_human`, task relevance, and the ranking of the bounded follow-up options
are written as an assessment; the action target and user-permission context are
included in the redacted task context so Jev cannot assess a generic action
detached from the proposed target. The adapter never invokes `up`, `restart`,
`down`, or `init adopt`; after the person chooses, the development agent maps
the fixed ID to the documented CLI command, runs it, and refreshes status.

To assess an already written request, pass `--preflight <path>` together with
a newly captured `--status <status-json>`; this rechecks the worktree revision,
target service or draft, freshness, and draft hash before the agent uses it.
Status files older than 30 seconds are rejected, and preflight requests expire
after two minutes. Passing only `--preflight` checks the request's own expiry;
the agent should prefer the status-backed check immediately before acting.

## Fixed option vocabulary

The adapter and skill share one closed option vocabulary. An artifact can
publish a smaller event-specific subset, but neither Jev nor the agent may
invent an ID or a command:

| ID | Current mapping | Permission boundary |
| --- | --- | --- |
| `inspect_logs` | `oberth logs <service> --once --json` | Read-only |
| `inspect_port` | Refresh `oberth status --json` | Read-only |
| `inspect_dependency` | Refresh `oberth status --json` | Read-only; never changes `machine:` |
| `inspect_manifest` | Read `oberth.yaml` and refreshed draft metadata | Read-only |
| `restart_service` | `oberth restart --only <service> --json` | Person must choose |
| `start_service` | `oberth up --only <service> --json` | Person must choose |
| `stop_service` | Project-wide `oberth down --json` | Person must choose; stops all option-berth services |
| `adopt_manifest` | `oberth init adopt` for a person-selected draft | Person must review and explicitly approve the write |
| `continue_without_action` | No control command | Leaves runtime unchanged |
| `ask_user_for_context` | Ask a follow-up question | No command |

`stop_service` is project-wide because the current CLI has no service-scoped
`down`; it must never be used to touch a machine dependency. `adopt_manifest`
is included in the shared vocabulary for a manifest-related handoff but is not
an automatic fallback: the adapter does not create, select, or write a draft.
The current deterministic service-failure artifacts expose only options that
match their facts; adding an option to `authorized_options` cannot make it
appear.

## Isolated acceptance

After `mage build`, run `scripts/verify-attention-handoff.py` from the
repository root. Without arguments it pauses at the same fixed-option choice
that the development agent presents to a person; `--choice` accepts one of the
four offered IDs (`inspect_logs`, `inspect_manifest`, `restart_service`, or
`continue_without_action`) for a repeatable check. The harness creates a temporary worktree and
`BERTH_HOME`, verifies the first failed start, the no-key adapter fallback,
the selected CLI mapping, the refreshed status, and cleanup. It never uses the
user's project ledger or services.

This script is a protocol and isolation harness, not an agent runtime. It does
not claim that a coding agent discovered the file or rendered the question to a
person; the agent-facing read/refresh/choice contract is covered by the
distributed skill, while the real fact and command transitions are exercised
against the built CLI here. Jev's remote behavior is tested with an injectable
HTTP server, and a live Jev call remains optional.
