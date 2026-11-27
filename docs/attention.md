# Attention: notes, and optional model triage

English | [中文](attention.zh.md)

Attention comes in two layers. Attention Notes are deterministic facts about a worktree: they are always produced, stored on this machine, and readable with no key, no network and no consent. Above them sits an optional model triage that judges whether a note affects the current task and needs a person's choice. Neither layer establishes process truth, runs a command, fixes a service or grants permission. `status`, `up` and `down` work with both layers off.

## Layer one: deterministic notes

Every Runtime World reduction produces notes. They follow a small lifecycle — `open`, `triaged`, `delivered`, `resolved` or `suppressed` — and resolve themselves once the worktree becomes healthy again. The Runtime Service writes them owner-only (`0600`, atomic rename, cross-process lock) to `BERTH_HOME/attention/notes/<worktree_id>.json`, so another process reads exactly what was derived, not a second opinion.

```sh
oberth attn notes
oberth attn notes --all --json
```

`notes` reads what the Runtime Service persisted, and when no host has persisted anything yet it derives the notes live from `oberth runtime inspect`. `--all` adds resolved history, `--project` reads another worktree, `--json` prints the note document (`oberth.attn.notes/v1`). `oberth attn status` adds a `notes` summary whenever notes exist.

## Layer two: opt-in model triage

With consent, a redacted causal packet is sent to the configured provider: hashed worktree identity, bounded branch and task text, the root causes, drifts and loops from the diagnosis, the kind of process holding a port — never its PID or command line — and one bounded error line with secrets and paths redacted.

| Mode | What the triage may add |
| --- | --- |
| `strict` | Nothing beyond facts and evidence; no action recommendation |
| `assist` | Urgency, `needs_human`, `task_relevant`, a bounded summary and one recommended fixed option |
| `reconcile` | The same, plus `auto_reconcile_eligible` when every safety gate holds |

`reconcile` marks a note eligible only for a local restart, and only when all gates hold at once: the recommended safety class is `local_restart`, `needs_human` is at most 0.25, confidence is at least 0.80, the service is not in a crash loop, and no foreign process holds the port. Eligibility is still not an action: nothing in this path kills a process oberth did not start, writes a manifest or widens an authorization.

## Inspect before you enable

Both commands below read local state only; neither calls a provider.

```sh
oberth attn status
oberth attn assess --dry-run
```

`status` reports the effective configuration and consent state and never the key. `assess --dry-run` prints the exact redacted request, so you can decide whether runtime information should leave this machine at all. Redaction is not a substitute for reading your own task text.

`on` and a passing doctor row mean assessment is allowed — not that a session is being observed, that a finding reached your coding agent, or that a person agreed with the advice. `oberth attn status` keeps those apart: it reports the last assessment state when there is recent activity, whether the project's committed `.claude/settings.json` enables oberth's hooks, and whether delivery leases for this worktree were consumed. `--quality` prints the same ledger in ordinary text. Model calls and cache hits do not prove that Attention helped.

## Configure and consent separately

`oberth attn config` is the only verb that writes credentials, and it is not consent. Run with no flags on a terminal it asks three questions with the arrow keys — provider, model, key — and every list ends with a custom row, so an endpoint or model the build does not list can still be configured. A preset fills a convention, never a restriction.

| Preset | Protocol | Endpoint |
| --- | --- | --- |
| `openrouter` | `decisions` | `https://openrouter.ai/api/alpha/decisions` |
| `typesafe` | `decisions` | `https://api.typesafe.ai/v1/systemone` |
| `openai` | `openai_chat` | The provider's chat-completions endpoint |
| `anthropic` | `anthropic` | The provider's messages endpoint |
| `ollama` | `openai_chat` | Loopback `http://127.0.0.1:11434/v1/chat/completions` |

`--protocol` (or `BERTH_ATTN_PROTOCOL`) selects the wire protocol; `decisions` is the default. A remote endpoint still needs a key, and without one the answer is `no_key`. The Ollama preset is the exception: a loopback endpoint needs no key and reports `key_source: "loopback"`, so a local model works without a secret anywhere.

```sh
oberth attn config
oberth attn on
```

`config` sends one synthetic probe and writes `attn.json` only if the probe succeeds; a failed probe leaves the existing configuration untouched. The probe carries no worktree facts, but it does contact the provider and may incur its charges. `on` then flips the single consent boolean, and the mode comes from `--mode` on `config` and `on` (or `BERTH_ATTN_MODE`), defaulting to `assist`. `BERTH_ATTN_KEY`, `BERTH_ATTN_ENDPOINT` and `BERTH_ATTN_MODEL` only supply fields; they never grant consent. The explicit process opt-in `BERTH_ATTN=1` and the persisted `enabled` setting have the precedence documented in the [adapter reference](agent/attn-adapter.md#an-environment-key-is-a-secret-not-consent).

Never put keys in command arguments, Git, task-context JSON, service logs or feedback. For unattended setup, read `attn config --help` for the stdin and environment forms instead of copying a key into shell history. A model name or endpoint preset is convenience, not proof that your account has access.

## What may leave the machine

Eligible requests carry the fields listed under layer two and nothing else: no full logs, no environment values, no keys, no unrelated worktrees, no port holder identity. `include_error_line` defaults to true and sends one redacted error line; set it to false in `attn.json` to suppress error lines entirely. Check the actual dry-run output rather than a hypothetical example.

The task context must authorize a subset of the fixed option vocabulary. Options in a note are candidates, not authorization, and model output is accepted only against that vocabulary — it cannot create a shell command or enlarge the scope. A suggestion with side effects still needs the person's decision.

## Mid-turn delivery in Claude Code

`oberth agent claude-hook` answers `PostToolUse` alongside `UserPromptSubmit`, `Stop`, `StopFailure` and `SessionEnd`. `PostToolUse` is the mid-turn path: it takes no delivery lease, skips the key derivation and consent work of a task boundary, reads this worktree's notes file under the lock, and emits `hookSpecificOutput` with at most 500 runes of `additionalContext`. It delivers each `(note, world_revision)` pair once and stays silent under `OBERTH_CLAUDE_TASK_MODE=edit` or when the worktree has no notes file.

When `--service` and `--run` are omitted and the worktree has a valid `oberth.yaml`, the hook scopes itself to the manifest's declared services; an explicit selector always wins, and a missing manifest still returns `scope_required`. `--print-config` includes the `PostToolUse` entry without `onFailure`, so a slow disk can never block a tool call. Review the printed JSON and merge it into the project's `.claude/settings.json` yourself; nothing writes that file for you. The full hook contract is in the [CLI reference](cli/handoff.md#native-claude-project-hooks).

## Failures are not permission

Missing keys, refused requests, rate limits, network failures, timeouts, malformed answers and stale revisions leave the fact-only path usable, and the agent can explain the missing advice or ask the person directly. Triage runs on a small asynchronous pool: two workers with per-worktree coalescing, a compare-and-swap on stale revisions, and a circuit breaker that opens for 60 seconds after three consecutive transport failures. A note whose revision moved is not overwritten by an older answer.

Probabilities are not claimed to be calibrated and are no guarantee of safety. The event connection schedules eligible fact reads; it is not a global watcher and does not promise a model call for every event. Implementation, fixed reasons and quality telemetry live in the [adapter reference](agent/attn-adapter.md).

## Turn it off

These are alternatives: the first disables triage, the second also forgets the stored key. Neither calls a provider.

```sh
oberth attn off
oberth attn off --forget
```

Turning triage off keeps the deterministic notes and the runtime facts they come from, and changes no service control. Local quality records are not uploaded automatically; review the adapter reference before sharing them.
