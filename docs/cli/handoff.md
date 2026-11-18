# Handoff

English | [中文](handoff.zh.md)

Work records, the optional model assessment, and handing the session to an external coding agent. These commands record or hand over work; they do not start or stop services.

## oberth workplace

The handoff layer's work record: what was done under a worktree and how it turned out, for humans and
agents to read back and take over. The full contract (subcommands, block fields, how records are
produced and their boundaries) is in [Work records](../workplace.md).

## oberth attn

The optional assessment is the `oberth attn` command; the consent model and what may leave the
machine are in [Attention](../attention.md). An agent should complete status reading, abnormality
verification, assessment and HIL output in a single invocation:

~~~sh
# for short tasks pass arguments directly, no need to write a file
oberth attn assess --project "$PWD" \
  --task "keep the API healthy while the integration test runs" \
  --authorize inspect_logs,inspect_port,restart_service,continue_without_action,ask_user_for_context
~~~

`oberth attn config` is the only verb that writes credentials. Run with no flags on a terminal it
asks three questions with the arrow keys — provider, model, key — and every list ends with a custom
row, so an endpoint or model the build does not list can still be configured. A preset is a
convenience, not a provider restriction: `openrouter` fills
`https://openrouter.ai/api/alpha/decisions` and defaults to `typesafe/jev-1.14`, and its list carries
the current decision models that endpoint serves (TypeSafe Jev, OpenAI GPT-6 Luna Decisions, Google
Gemini Decision 1, Microsoft Decision 1, Perplexity Decider, Cloudflare Clef, LiquidAI d1.1, Upstage
Solar Decide, Morph Reflex); `typesafe` fills `https://api.typesafe.ai/v1/systemone` with
`jev-latest` and `jev-preview`. Scripts and CI keep the flag form: an endpoint and model directly,
or `--preset NAME`, with the key from `--key-stdin`, `--key-env NAME`, hidden terminal input, or
the already stored key. Either form sends one synthetic redacted probe and only writes `attn.json`
when the probe succeeds — a failed probe leaves the existing configuration untouched. `config`
changes credentials, not consent. `oberth attn on` and `oberth attn off [--forget]` flip the single
`enabled` boolean and never touch the network. `oberth attn test` probes the current effective
configuration once. `oberth attn status` reports the resolved state, the fixed reason when it cannot
be used, and a summary of the local quality ledger. `--json` carries the decision document: the
consent and configuration fields plus a `quality` object reduced to the counters that answer "is a
runner observing, and is anything happening" — `activity`, `records_read`, `assessment_records`,
`model_calls`, `cache_hits`, `feedback_records`, `unique_cases`, `labeled_cases`,
`unlabeled_cases` and `sessions`. `--quality` replaces that object with the full quality report
described below; `--since` and `--quality-project` narrow that report, so they imply `--quality`.
A zero count stays in the document instead of disappearing. `oberth doctor` gains one offline row
for the same state, and a human `status` read mentions the feature once every two weeks when it is
off and an anomaly is present; `BERTH_NO_TIPS=1` silences that hint.

`oberth attn assess` is the one-shot runner. `--dry-run` prints the exact redacted
request for the current event without sending it, and `--follow` keeps the same
loop alive for a task, emitting NDJSON (`subscribed`, only changed handoffs, and
`stopped` on cancellation). `oberth attn feedback` labels what a person actually
decided, and `oberth attn preflight` derives a bounded action request from a fresh
status snapshot. There is one `oberth` binary; none of these is a separate
executable.

The context file contains `task` and the required `authorized_options`, and is **written by the agent
itself**: nothing on the machine generates it for you, and the path is chosen by the agent too
(parallel tasks each use their own file; do not share one `/tmp` path). `--task` and `--authorize`
override the same-named fields in the file; when neither is given the runner errors out directly and
will not guess an authorization set on the agent's behalf. The adapter sends only a redacted fact
summary to the selected provider and does not read logs or start and stop services. The runner
outputs `clear`, `continue`, `human_required`, `unavailable` or `stale`; on success it atomically
writes the validated `assessment` back to the same file and re-reads the state before returning. When
there is no usable environment endpoint, model or key, or on network failure,
invalid response, expired file or changed state revision, the fact file is left unchanged. The
`~/.option-berth/attn.json` written by `oberth attn config` (`$BERTH_HOME/attn.json` for isolated
runs) is read automatically by `oberth attn` and is the only persistent consent: `enabled: true`
turns the model on, `enabled: false` keeps it off even when a key is present. An explicit
`BERTH_ATTN_ENDPOINT` / `BERTH_ATTN_MODEL` / `BERTH_ATTN_KEY` supplies those fields but does not
enable the model; `BERTH_ATTN=1` enables it for one process. Each unavailable answer carries one fixed
reason (`no_key`, `disabled`, `invalid_config`, `unauthorized`, `rate_limited`, `timeout`, `network`,
`provider_error`, `bad_response`, `stale`) rather than a provider's error text. `assess` and
`assess --follow` must be given `authorized_options` explicitly; the artifact's `options` is only a
candidate set and does not automatically become the agent's authorization. Full arguments and the
privacy boundary are in
[`attn-adapter.md`](../agent/attn-adapter.md).

`oberth attn assess` / `assess --follow` leave redacted local quality observations in
`$BERTH_HOME/quality/events.ndjson`. After a real interaction, a development agent can write back
human labels and action outcomes with `oberth attn feedback --file <json>`, then use
`oberth attn status --json --quality [--since 168h] [--quality-project <name>]` to view label
coverage, assessment availability, routing reasons and the model/option distribution,
human-escalation precision/recall/F1, the model's
`needs_human` / `task_relevant` probability error, recommendation hit rate, action success rate,
resolution rate and detection latency under `quality`. Quality records are not `status` facts and
change no execution permission; the `sessions` object explains the number of sessions, polls, events,
the model calls, cache hits and no-event polls, and is part of the default summary as well; with no
real feedback the metrics stay `null`.

`on` means permission to assess, not that a task session is running. A development skill may attach
one existing runner to a relevant runtime task after binding its canonical worktree, redacted task
summary and fixed evaluation options. It must reuse its own task handle, close it on task exit, and
never create global monitoring or grant permission for side effects. The runner checks consent and
the configured provider identity before every poll; disabling assessment or changing the endpoint,
model or credential ends that task session before another request (`disabled` or
`configuration_changed`). No eligible anomaly means no provider request.

`assess --follow` reuses an existing scoped change stream when available, filters to the exact task worktree and deduplicates its cursor. Relevant changes trigger a status/artifact refresh, not an automatic model request. Reconnection requests a fresh snapshot; connected sessions also reconcile at `max(10s, --interval)`. `--interval` controls local consent checks and fallback polling, not guaranteed model cadence. Absent/older/disconnected services use bounded status polling without starting a runtime.service. No separate events watcher is needed for the same task. Normal state still makes no provider request, and post-assessment freshness checks remain mandatory.

`quality.model_calls` and `quality.cache_hits` count recorded transport attempts and cache reuse,
including one-shot assessments and sessions that have not ended; lifecycle summaries are not added
twice. Validation and status-read failures do not count as model calls. Local errors are recorded
without their private error text. Old cumulative session totals are reused only for an all-time or
fully covered session window; `legacy_counts_unallocated: true` flags older totals that cannot be
located inside a partial window, so displayed counts are lower bounds. `--quality-project` accepts
a label (which may aggregate names) or an exact worktree root/identity.

`quality.activity` separates consent from observation. Its `state` is `no_session` (no observation
record in this home/window/filter), `observing` (a completed task poll has an unexpired lease),
`failed` (the last observation failed), or `recent` (history only, no fresh lease). It includes
`observing_sessions`, `last_observed_at`, `last_status`, `reason`, and `no_event`. Observation leases
last between 15 and 60 seconds depending on the polling interval; they are evidence of a recent
completed poll, not proof of process liveness. A historical `subscribed` record is never an active
session. Each poll leaves a local `observation` or `assessment` record even when NDJSON output is
unchanged; no-event observations do not inflate `assessment_records`. Rotation remains bounded,
so an absent record is not a provider billing statement or proof that a request never occurred.

## oberth agent: external coding-agent handoff

`agent list --json` lists OpenCode, Codex, Claude Code, DeepSeek Harness and Pi without executing them.
`agent plan codex --worktree <path> --json` returns an exact executable/argv/cwd plan; it does not launch,
log in, install hooks, modify configuration, or send a model request. `--prompt-stdin` is plan-only and
avoids putting the initial prompt in shell history. The resulting JSON can contain that prompt: keep it local.
`agent run codex --worktree <path>` explicitly replaces the CLI with the native agent on macOS/Linux.
The agent keeps its own permissions, authentication, tools and conversation history; no approval bypass is added.

Native prompts are supported for OpenCode, Codex, Claude Code and Pi. DeepSeek uses `dsh --profile tui`
for an already installed custom native profile; it is not a shipped-profile guarantee. Use `--mode task`
with a prompt to select its documented headless profile. That one-shot task is not a resumed native conversation.
No implicit “resume last” crosses worktree boundaries. The selected cwd is not an operating-system sandbox.
Windows currently supports plan/list, not native process handoff. Merely finding a binary does not validate login
or a particular installed version; a provider failure remains a provider failure, never a silent shell fallback.

## oberth agent stream

`oberth agent stream <provider> --worktree <path> [--resume-file <path>]` starts the structured bridge
process: one command per line on stdin, one event per line on stdout, both NDJSON carrying `"v":1`;
stderr carries diagnostics only. The contract is in `engine/docs/schema/agent-stream.schema.json`.
`agent list --json`'s `"stream": true|false` says whether this build includes the streaming adapter for that provider (Codex, Claude Code, OpenCode ACP, Pi RPC).
Production requires the corresponding CLI to be installed; automated tests may set `BERTH_AGENT_STREAM_FAKE=<provider>` to use a pure-NDJSON fake driver, or inject a
`LookPath` pointing at the `engine/internal/agentstream/testdata/fake*` stand-ins for `go test`.
This build includes live bridges for Codex `app-server`, Claude `stream-json`, OpenCode `acp` and Pi `--mode rpc`; when the CLI is not installed, the first `turn.start` returns `unsupported`.

## Explicit native continuation

`oberth agent plan <provider> --worktree /workspace/project --resume-file /absolute/session-file --json`
produces a structured continuation plan; `agent run` accepts the same file option and hands the
terminal to the provider. There is no implicit `--last`, partial ID search, shell interpolation,
automatic login, conversation import, or approval bypass.

Supported metadata: Codex JSONL `session_meta` header; Claude Code JSONL records containing
`sessionId`, `cwd`, and a semantic `version`; Pi session-format v3 UUID headers; OpenCode JSON
exports with `info.id`, `info.directory`, and `info.version`. Metadata reads are bounded to 256 KiB;
transcript messages are not copied into plans or another conversation database. The recorded
worktree must resolve to the same directory as `--worktree`. Plan `resume` reports the exact ID,
source metadata version, source file, worktree, and `approval_owner: provider_native`.

The person must select an original provider session file or a trusted OpenCode export. This checks
that file's metadata, not the authenticity of a user-edited export or the currently installed CLI's
version. The external CLI remains responsible for finding the ID, enforcing permissions and
reading its actual conversation. Unknown formats, task-mode continuation and DeepSeek file-based
continuation fail closed. This is a structured **launch** contract, not a unified live event/approval
protocol; those capabilities remain in backlog.

Provider contracts: [OpenCode CLI](https://opencode.ai/docs/cli/),
[Codex CLI](https://developers.openai.com/codex/cli/reference/),
[Claude Code CLI](https://code.claude.com/docs/en/cli-reference), and
[Pi CLI](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli.md).
