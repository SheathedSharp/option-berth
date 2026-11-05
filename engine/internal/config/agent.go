package config

import (
	"strings"
	"time"
)

// The agent block configures `oberth init draft`: the person's own local
// coding agent drafts a oberth.yaml, and a person adopts it.
//
// option-berth never calls a model. This block says which CLI to run and how
// long it may take; it holds no credential. The CLI brings its own auth, and
// `agent.args` is where a permission posture the user chose belongs —
// option-berth adds none of its own, so a run starts with the CLI's defaults.

// DefaultAgentTimeout bounds one drafting run. It is expressed in minutes
// because the agent reads the project and may build it and try services.
const DefaultAgentTimeout = 20 * time.Minute

// AgentConfig holds the drafting agent's settings.
type AgentConfig struct {
	// Command is the agent CLI to run, by program name (claude, codex).
	// Empty means auto-detect: the one supported CLI on PATH.
	Command string `yaml:"command"`
	// Args are extra CLI arguments inserted before the prompt. A sandbox
	// mode, `--allowedTools`, or a bypass flag the user chose goes here.
	Args []string `yaml:"args"`
	// Model is handed to the CLI through its own flag (claude `--model`,
	// codex `-m`). Empty uses the CLI's default. Names are not portable
	// between CLIs, which is why `command` names one CLI at a time.
	Model string `yaml:"model"`
	// Timeout bounds one drafting run as a Go duration ("20m"). Empty means
	// DefaultAgentTimeout.
	Timeout string `yaml:"timeout"`
}

// ResolvedTimeout returns the drafting ceiling, falling back to
// DefaultAgentTimeout when the setting is absent or unusable.
func (a AgentConfig) ResolvedTimeout() time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(a.Timeout))
	if err != nil || value <= 0 {
		return DefaultAgentTimeout
	}
	return value
}
