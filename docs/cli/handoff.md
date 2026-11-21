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

Task-context JSON accepts `services` and `run_ids` evaluation filters. A service selector follows only the declared service; an ad-hoc event marked `run_only` needs an exact run ID. Filters enter the assessment cache identity. Omitting both keeps the existing declared-service scope, not arbitrary ad-hoc runs.

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

## Native Claude project hooks

`agent claude-hook` handles Claude Code's official `UserPromptSubmit`, `Stop`, `StopFailure` and `SessionEnd` JSON envelopes. The adapter has native non-interactive (`-p`) end-to-end evidence with Claude Code **2.1.322 on macOS** and local fake providers; earlier versions and native Windows execution are not verified/supported by this adapter. It is not a transparent attachment to arbitrary running `claude`/`cld` processes. A wrapper must preserve project hooks.

Interactive TTY and the user’s private `cld` wrapper are not verified. A clean-HOME interactive PTY attempt exited before task execution: Claude’s first-run connectivity check still targeted `api.anthropic.com` despite a local provider base URL and failed through the deliberately refusing proxy. No task or hook feedback ran in that attempt; do not infer interactive coverage from `-p` evidence or bypass project trust/onboarding to claim it.

Enable once per project, only after reviewing its selected scope and read-only evaluation options:

```sh
oberth agent claude-hook --project "$PWD" --service api \
  --authorize inspect_logs,inspect_port,inspect_manifest --lease 10m --print-config
```

The command only prints JSON. `--print-config` also requires `--project` and does not infer the current directory. Review the JSON and merge its `hooks` into the selected project's `.claude/settings.json` without replacing unrelated settings; start a new native Claude session under the tool's normal project-trust rules. It does not write settings, install a global hook or change Attention consent. Keep `UserPromptSubmit`'s `onFailure: "block"`: a failed/timed-out task boundary must block that prompt rather than let a later Stop consume a previous task's lease; this uses the verified version's official failure policy. A minimal configuration is in [the example](../../examples/claude-hooks/settings.example.json); prefer generated output when using a nonstandard executable path. A matching installed skill is not evidence that these hooks ran. `oberth doctor --only claude_hooks` is a read-only check of the project's committable `.claude/settings.json`; it never reads `settings.local.json` or the user-global Claude config, and it never writes hooks. If that file already enables the hook command but hard-codes a `timeout` below 20 seconds, or passes a `--lease` shorter than the hook command budget, the row warns: a newer oberth binary cannot stop Claude from cutting a shorter hook timeout first. Omitting `timeout` keeps Claude's official defaults (30 seconds for UserPromptSubmit, 600 seconds for Stop) and is not warned as a short budget.

`--project` is mandatory and must resolve to the hook input's exact worktree. `--service` selects declared services; `--run` selects exact already-recorded run IDs, including portless workers. Both accept comma-separated values. Same-named foreign processes and replacement ad-hoc generations are never adopted. `--authorize` is an explicit subset of fixed read-only evaluation options, not tool/action approval. Task scope is fixed for the lease; associate a newly created ad-hoc run explicitly in later project/task configuration. No manifest is generated, no home directory scanned, and no missing service started. An already-running Runtime must know the selected project; an absent service, unregistered project or missing Runtime context returns `runtime_context_unavailable` rather than switching to a system scan. When the cause is known, the reason is qualified as `runtime_context_unavailable:runtime_service_required`, `runtime_context_unavailable:project_not_registered_in_runtime`, or `runtime_context_unavailable:timeout`. Unknown Prepare failures stay unqualified so raw RPC text is not copied into the hook message. The hook reads no source, git diff or log contents. A task that needs services still uses the ordinary authorized `up`/`start` path; healthy existing runs are reused as facts.

`UserPromptSubmit` binds canonical worktree, opaque session identity and a fresh task generation. It injects only selected Runtime facts, including observation coverage: a blind/narrow listener view does not become `running:false`. Exact task runs read the existing full exit facts, not the status display's five-row cap. The hook ignores `prompt`, `transcript_path`, assistant messages and error details. The assessment summary is a fixed conservative development-task label, so relevance has less task detail than a manually supplied redacted summary; conversations are neither stored by the adapter nor used as assessment summaries.

With complete task scope, explicit options and existing Attention consent/configuration, a bounded child observer reuses the existing runner, scoped change feed, reconnect, cache and quality ledger. Filtering precedes model requests; no eligible event means no model request. `on` alone starts no observer and promises no unlimited model budget. Selected Runtime facts still enter the task if Attention is off; its missing permission/configuration or options are reported separately. Missing scope gives `scope_required`; `OBERTH_CLAUDE_TASK_MODE=edit` disables observation and context for a pure-edit session with `pure_edit`. The adapter does not infer runtime intent by reading the prompt. A project's explicit service scope otherwise applies conservatively to each submitted task.

At `Stop`, the hook cancels and joins only its own observer, then checks consent, configuration identity, scope, event revision and freshness again. Both relevant read-only recommendations and human-required/unavailable findings can be returned. Top-level official `decision: "block"` / `reason` JSON gives the same native Claude task **at most one** bounded reporting/inspection continuation; `systemMessage` reports the local state. No keystrokes, transcript scraping, new tool permissions or automatic service control are involved. Consumption is recorded before output: a crash can lose feedback, but cannot replay it. `stop_hook_active` and the consumed cursor prevent loops; a native answer is not proof that the finding was fixed.

`StopFailure` and `SessionEnd` close without continuation. A new submitted task replaces and joins the old generation. The default and maximum non-renewable lease is `10m` (`--lease` can shorten it). A lost or killed observer is never resumed. If a pending finding is still fresh when the observer lease expires, `Stop` may still deliver it; a later `UserPromptSubmit` in the same session delivers an unconsumed previous finding into the new prompt context before starting the next generation. Stale pending findings are discarded. The hook command budget and `--print-config` `timeout` share one value that exceeds the Attention client timeout, so `Stop` can join an in-flight assessment that still holds `active.lock`. A successful delivery writes lifecycle telemetry only; it does not invent a human or Attention feedback label. An interrupt that supplies no Stop/SessionEnd is reclaimed by the next prompt or lease deadline, not claimed as an immediate callback. Cleanup never stops shared services. Only small owner-private delivery cursors live under `BERTH_HOME/attention/claude/`; they are not another runtime registry. Removing project hooks does not synchronously signal an already-owned lease; disable Attention to revoke assessments, or let that bounded lease close. Session-wide or global hooks are not required.



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
