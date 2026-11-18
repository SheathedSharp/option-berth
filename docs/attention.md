# Optional model advice: Attention

English | [中文](attention.zh.md)

Attention helps a coding agent decide whether a published runtime anomaly matters to the current task and needs a person's choice. It does not establish process truth, run commands, fix services or grant permission. `status`, `up` and `down` work without it.

## Inspect before enabling

These operations do not call a provider:

```sh
oberth attn status
oberth attn assess --dry-run
```

`status` reports the effective configuration and consent state, never the key. `assess --dry-run` shows the redacted request. Review that request before deciding whether to send runtime information outside the machine; redaction is not a substitute for checking your task text.


For native Claude/cld, explicit project hooks can attach the existing runner and return a fresh finding through the official Stop channel. Enable the project once; `on` or a skill alone cannot attach an arbitrary running agent. See the [native hook contract](cli/handoff.md#native-claude-project-hooks).

## Configure and consent separately

Run the following only after deciding to configure a provider. `config` asks for provider, model and hidden key input, then makes one synthetic network probe before saving an owner-only settings file. The probe contains no worktree facts, but still contacts the provider and may incur its charges. If it fails, the existing settings are not replaced. Configuration alone does not enable real assessments.

```sh
oberth attn config
oberth attn on
```

`on` enables local consent; it is not permission for an agent to restart a service or send unrelated data. `BERTH_ATTN_KEY`, endpoint and model environment settings only supply configuration. They do not grant consent. The explicit process opt-in `BERTH_ATTN=1` and persisted `enabled` setting have the precedence documented in the [adapter reference](agent/attn-adapter.md#an-environment-key-is-a-secret-not-consent).

Never put keys in command arguments, Git, task-context JSON, service logs or feedback. For unattended configuration, consult `attn config --help` for stdin/environment input instead of copying a key into shell history. A model name or endpoint preset is a convenience, not proof that your account has access.

## What may leave the machine

Eligible requests contain bounded event summaries, hashed worktree identity, limited branch/task text, state revision, evidence references and authorized fixed option IDs. They do not include full logs, environment values, keys or unrelated worktrees. Inspect the actual dry-run output, not a hypothetical example.

The task context must explicitly authorize a subset of supported options. Options in an artifact are only candidates. Model output is accepted only against that vocabulary; it cannot create a shell command or enlarge the authorization scope. A suggestion with side effects still requires the person's decision.

## Failures are not permission

Missing keys, refused requests, rate limits, network failures, timeouts, malformed answers and stale state leave the fact-only path usable. The agent can explain the missing advice and ask the person directly. A delayed assessment is checked against fresh facts before use. Probabilities are not claimed to be calibrated and are not guarantees of safety.

The event connection schedules eligible fact reads; it is not a new global watcher or a promise to call a model for every event. Follow/subscribe mode belongs to the current task and its allow-list, not all projects. Implementation, fixed reasons and quality telemetry are documented in the [adapter reference](agent/attn-adapter.md).

## Turn it off

These are alternatives: the first disables assessments, and the second also forgets the stored key. Neither operation calls a provider.

```sh
oberth attn off
oberth attn off --forget
```

Disabling advice does not delete the underlying runtime facts or change service control. Local quality records are not automatically uploaded; review the adapter reference before sharing them.
