package config

import (
	"strings"
	"time"
)

// The agent block configures `oberth init draft`: the user's own local
// coding agent drafts a oberth.yaml, and the person adopts it.
//
// option-berth never talks to a model itself. This block says which CLI to run
// and how long to give it — nothing here is a credential. The agent brings its
// own auth, and `agent.args` is where a permission posture the user chose goes;
// option-berth adds none of its own, so the run starts with the CLI's defaults.
const (
	// DefaultAgentTimeout is the ceiling on one drafting run. It is minutes,
	// not seconds: the agent reads the project, and may build it and try
	// services to see whether they come up.
	DefaultAgentTimeout = 20 * time.Minute
)

// AgentConfig holds the drafting agent's settings.
type AgentConfig struct {
	// Command names the agent CLI to run, by program name (claude, codex).
	// Empty means auto-detect: the one supported CLI on PATH.
	Command string `yaml:"command"`
	// Args are extra arguments for the CLI, inserted before the prompt. This
	// is where `--allowedTools`, a sandbox mode, or a bypass flag goes, for
	// someone who wants the agent to be able to build and try services.
	Args []string `yaml:"args"`
	// Model is the model name to hand the CLI, through whatever flag that CLI
	// spells it with (claude `--model`, codex `-m`). Empty means the CLI's own
	// default. It is one setting rather than one per CLI because `command`
	// names one CLI at a time; the names are not portable between them, and
	// the panel says so.
	Model string `yaml:"model"`
	// Timeout is the ceiling on one drafting run, as a Go duration ("20m").
	// Empty means DefaultAgentTimeout.
	Timeout string `yaml:"timeout"`
}

// ResolvedTimeout returns the drafting ceiling, falling back to
// DefaultAgentTimeout.
func (a AgentConfig) ResolvedTimeout() time.Duration {
	v, err := time.ParseDuration(strings.TrimSpace(a.Timeout))
	if err != nil || v <= 0 {
		return DefaultAgentTimeout
	}
	return v
}
