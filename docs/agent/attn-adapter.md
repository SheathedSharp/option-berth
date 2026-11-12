# Attention adapter

`oberth attn` is an optional, read-only assessment of the agent attention handoff.
Its `assess` runner reads `oberth status --json --no-mark`, validates the exact
published artifact, asks the selected provider whether the event affects the
current development task and needs the user's choice, then stores only the
validated typed assessment in the same artifact. It never reads service logs,
starts or stops a process, edits a manifest, or invents a command.

The verbs map one-to-one onto the standalone binary's flags, and the JSON is the
same object the agent already parsed:

| `oberth attn` | standalone shell |
|---|---|
| `assess` | `jev-attention --auto` |
| `session` | `jev-attention --session` / `--subscribe` |
| `feedback --file` | `jev-attention --feedback` |
| `report` | `jev-attention --quality-report` |

`engine/cmd/jev-attention` remains as a thin shell over the same runner package
(`internal/attn/runner`) while existing scripts and the installed skill catch up.

The adapter speaks one wire contract: a structured `state`, `model` and typed
`questions` request, answered with typed `answers`. TypeSafe's endpoint and
OpenRouter's Jev Decisions API both implement it; any other endpoint that
implements the same shape works too. Endpoint, model and key are independent
fields — the `openrouter` and `typesafe` presets only fill the first two while
you configure, and are never consulted at runtime. The adapter uses two `noul`
questions (`task_relevant`, `needs_human`) and a `choice` question for the
authorized fixed options. See the [OpenRouter Jev guide](https://openrouter.ai/blog/insights/what-is-jev/)
and [TypeSafe API reference](https://docs.typesafe.ai/api/) for the upstream
wire contract.

## Turning it on

`oberth attn enable` is the one step, in this order: give an endpoint and model
(directly, or through a preset that fills both), read the key, prove the key
answers, then write the owner-only file.

```sh
# a script or CI job: the key never appears in an argument list
printf '%s' "$KEY" | oberth attn enable --preset openrouter --key-stdin

# read it from an existing variable
oberth attn enable --preset typesafe --key-env ATTN_KEY

# any compatible endpoint: no preset, explicit fields
oberth attn enable --endpoint https://assess.example/decisions --model team/model --key-env ATTN_KEY

# a person at a terminal: hidden input
oberth attn enable --preset openrouter
```

The proof is one synthetic, redacted assessment — the same request shape a real
event uses, carrying no worktree facts. **If the probe fails, nothing is
written**, so a typo cannot replace a working configuration with one that breaks
every later task. On success the command prints what it stored and what will and
will not be sent.

| Command | What it does | Network |
|---|---|---|
| `oberth attn status` | resolved state: on or off, provider, key source, fingerprint, reason | no |
| `oberth attn enable` | store a key, probe once, then write the settings | once |
| `oberth attn test` | one synthetic probe against the current settings | once |
| `oberth attn preview` | the exact redacted body for this worktree's current event | no |
| `oberth attn disable` | turn it off, keeping the key; `--forget` removes it | no |

`status` never prints a key, only a short fingerprint so a person can tell two
keys apart. `preview` is the honesty check to run before enabling: if the body it
shows is not acceptable, do not enable.

Hidden terminal input uses the platform's own termios through `golang.org/x/sys`,
which the engine already depends on; `golang.org/x/term` is deliberately not
added for eight lines of the same work. On Windows interactive entry is refused
with a pointer to `--key-stdin` / `--key-env`, because the engine's Windows
surface is a native build and a smoke test, not full acceptance.

## Run it

For a task that keeps services running, prefer the task-scoped session form:

```sh
oberth attn session --project "$PWD" --task-context "${TMPDIR:-/tmp}/oberth-task-$$.json"
```

The path is the agent's to choose: nothing on the machine creates this file, so
give each task its own — a fixed `/tmp` name lets two parallel agents overwrite
each other's allow-list.

`session` is the long-lived form (`--subscribe` in the standalone shell). The
agent starts it after the initial status read and stops it with the task. It
polls facts continuously but calls the model only for a new event/revision or an
uncached assessment.

The agent-side runner performs the status read and exact artifact lookup in one
operation. Do not scan the `BERTH_HOME/attention` directory:

```sh
oberth attn assess --project "$PWD" --task-context "${TMPDIR:-/tmp}/oberth-task-$$.json"
```

Create a short task context outside the repository. The option list is an
explicit allow-list supplied by the development agent, not a model decision.
A short task can skip the file entirely with `--task "<summary>"` and
`--authorize <comma-separated ids>`; those override the same fields when both
forms are given, and the runner refuses to guess when neither is present:

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
oberth attn session --project "$PWD" --interval 2s \
  --task-context "${TMPDIR:-/tmp}/oberth-task-$$.json"
```

It emits NDJSON: one `subscribed` record, then only changed event/revision/
assessment handoffs, and `stopped` when the agent cancels the process. It polls
`oberth status --json --no-mark`, so the daemon and core CLI remain model free
and no hook or MCP registration is needed.

For CI and one-off runs the same three fields come from the environment:
`BERTH_ATTN_ENDPOINT`, `BERTH_ATTN_MODEL` and `BERTH_ATTN_KEY`. They override
the stored fields for that process only. Never put a key in a repository file or
a task-context JSON file.

### An environment key is a secret, not consent

A key in a shell profile does not mean the person asked for their runtime events
to be sent to a provider. Only two things turn the assessment on:

| Declaration | Scope |
|---|---|
| `"enabled": true` in the owner-only `attn.json` | every process that shares this `BERTH_HOME` |
| `BERTH_ATTN=1` (`true`, `yes` or `on`) | this process only, for CI and one-off runs |

Everything else only supplies settings: `BERTH_ATTN_ENDPOINT`,
`BERTH_ATTN_MODEL` and `BERTH_ATTN_KEY` change the field they name and never
grant consent. An `attn.json` that says `"enabled": false` is a decision the
person already made, so an environment key does not override it. One resolution
function decides all of this.

### Fixed reasons instead of provider text

Every "the model was not used" answer carries one of these codes, in the JSON output,
the quality ledger and `oberth attn test`:

`no_key`, `disabled`, `invalid_config`, `unauthorized`, `rate_limited`,
`timeout`, `network`, `provider_error`, `bad_response`, `stale`.

The code is what a caller branches on, so a person gets a next step written for
that code rather than a provider's error body. A failure this client does not
recognise is `provider_error`; it is never left blank.

`oberth attn enable` is the persistent setup path. It writes an owner-only file
at `~/.option-berth/attn.json` (or `$BERTH_HOME/attn.json` in an isolated run):

```json
{
  "schema": "oberth.attn-settings/v1",
  "enabled": true,
  "preset": "openrouter",
  "endpoint": "https://openrouter.ai/api/alpha/decisions",
  "model": "typesafe/jev-1.13",
  "api_key": "…",
  "timeout_ms": 10000
}
```

The adapter reads this file automatically, so an agent does not need to
re-export a shell variable for every task. This file is the only persistent
consent: an environment key supplies the secret but does not enable the model,
and `enabled: false` keeps it off even when a key is present in the environment.
The key never
appears in attention artifacts, quality telemetry, or daemon output.

For one-off use, `--task` and `--authorize inspect_logs,restart_service` can
replace `--task-context`. `--authorize` (or `authorized_options` in the JSON)
is required by `--auto` and `--subscribe`; artifact options are candidates,
not implicit authorization. A long-running agent may provide a broad subset of
the fixed option vocabulary; the runner validates every ID and sends only its
intersection with the current event's candidates. An empty intersection is an
error. `--endpoint`, `--model`, and `--timeout` override
the selected provider's endpoint/model and the ten-second default timeout.
For persistent local use, prefer the settings file; `--api-key` is provided
for test harnesses and can expose a secret in process listings. None of these
flags is consent: they change a resolved field, they do not enable the model.

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
the selected provider. Task text is length-limited
and obvious credentials and local paths are redacted. Full logs, environment
variables, API keys, and unrelated worktrees are never sent or copied into the
artifact. Model output is accepted only when its option IDs and probabilities
match the fixed artifact options; free-form text cannot enter the handoff.

The runner does not claim calibrated the model probabilities. Its fixed policy asks
the user for every side effect, incomplete or low-confidence result, close
option ranking, and relevant `continue_without_action`; only a clearly
separated, confident read-only inspection may return `continue`. A stale result
always requires a fresh status read.

## Local quality telemetry

The adapter keeps a local, owner-readable quality ledger at
`$BERTH_HOME/quality/events.ndjson`, or `~/.option-berth/quality/events.ndjson`
when `BERTH_HOME` is unset. `--auto` and `--subscribe` append a redacted
assessment observation after each new runner state. The record contains the
event/revision, fixed options, the model typed values, the conservative HIL decision,
and timestamps; it does not contain task text, log contents, absolute paths,
commands, environment variables, or API keys. The file is mode `0600` and is
rotated at 8 MiB, with one `.1` backup.

The observation alone cannot tell whether a person was actually needed. After
the agent has shown the request and, if applicable, executed the person's
choice, it should write a short feedback document without putting the task or
logs into it:

```json
{
  "schema": "oberth.attn.feedback/v1",
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
oberth attn feedback --file /tmp/oberth-attention-feedback.json --project "$PWD"
```

`human_needed` is the ground-truth label for the event: set it to `false` when
the runner asked for a person unnecessarily, and to `true` when the agent later
found that a choice was required even if the runner had returned `continue`.
`user_decision`, `chosen_option`, and `action_status` may be written in a later
feedback record for the same event/revision; the report merges the latest
values. Copy `event_id`, `attn_revision` (as `state_revision`),
`context_id`, and `worktree_id` from the latest runner record rather than from
an older status poll. Feedback never changes the attention artifact or grants
permission.

Inspect the accumulated evidence with:

```sh
oberth attn report
oberth attn report --since 168h --quality-project example-worker
oberth attn report --since 168h --quality-project example-api
```

The report includes session lifecycle and polling counts, observation/status counts, labelled/unlabelled-case
coverage, assessment availability, routing-reason and model/option breakdowns,
user decisions, the conservative human-routing precision/recall/F1, the model's
`needs_human` and `task_relevant` Brier scores and mean absolute errors,
recommendation matches, action success rate, resolution rate, and
event-to-observation latency. A metric is `null` until enough feedback labels
exist; the synthetic policy tests do not populate these production metrics.
Ledger write failures are diagnostic only and do not change the runner result.

## Action preflight

When an agent is preparing a side effect that is not itself a runtime fact, a
preflight request can be derived from a freshly saved `oberth status --json`
response. In this release it is still reached through the standalone shell:
`oberth attn` carries the runner verbs above, not the preflight flags, so the
command below is `jev-attention`, not `oberth attn`. The request is an external
adapter artifact under `BERTH_HOME/attention/preflight/`; it never enters the
daemon or replaces the runtime attention artifact:

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

The command writes the bounded request before contacting the model. Without a key or
when the model is unavailable it returns `status: "unavailable"` while leaving the
preflight request available for the agent to present directly. With the model, only
`needs_human`, task relevance, and the ranking of the bounded follow-up options
are written as an assessment; the action target and user-permission context are
included in the redacted task context so the model cannot assess a generic action
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
publish a smaller event-specific subset, but neither the model nor the agent may
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
appear. `inspect_dependency` stays in the vocabulary for artifacts that already
carry it, but no current event offers it: a declared `machine:` entry that is
not listening is a fact the person declared and `status` already shows, not an
anomaly to hand off.

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
against the built CLI here. the model's remote behavior is tested with an injectable
HTTP server, and a live the model call remains optional.
