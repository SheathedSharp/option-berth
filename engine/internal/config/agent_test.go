package config

import (
	"strings"
	"testing"
	"time"
)

// agent.timeout falls back to the default rather than ever leaving a drafting
// run with no ceiling, and never trusts a value that is not a positive
// duration.
func TestResolvedAgentTimeout(t *testing.T) {
	tests := []struct {
		name string
		set  string
		want time.Duration
	}{
		{"unset", "", DefaultAgentTimeout},
		{"explicit", "45m", 45 * time.Minute},
		{"zero", "0", DefaultAgentTimeout},
		{"negative", "-5m", DefaultAgentTimeout},
		{"nonsense", "soon", DefaultAgentTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (AgentConfig{Timeout: tt.set}).ResolvedTimeout(); got != tt.want {
				t.Errorf("ResolvedTimeout(%q) = %s, want %s", tt.set, got, tt.want)
			}
		})
	}
}

// A bad agent.timeout is dropped with a warning, like every other duration.
func TestValidateDropsABadAgentTimeout(t *testing.T) {
	cfg := &Config{}
	cfg.Agent.Timeout = "soon"
	warnings := validate(cfg)
	if cfg.Agent.Timeout != "" {
		t.Errorf("timeout = %q, want it dropped", cfg.Agent.Timeout)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "agent.timeout") {
		t.Errorf("warnings = %v, want one naming agent.timeout", warnings)
	}
}
