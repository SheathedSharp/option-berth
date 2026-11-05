package config

import (
	"strings"
	"testing"
	"time"
)

// A missing or unusable ceiling never leaves a drafting run unbounded.
func TestResolvedAgentTimeoutNeverDropsTheCeiling(t *testing.T) {
	cases := []struct {
		label string
		raw   string
		want  time.Duration
	}{
		{"absent", "", DefaultAgentTimeout},
		{"explicit", "45m", 45 * time.Minute},
		{"zero", "0", DefaultAgentTimeout},
		{"negative", "-5m", DefaultAgentTimeout},
		{"not a duration", "soon", DefaultAgentTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := (AgentConfig{Timeout: tc.raw}).ResolvedTimeout(); got != tc.want {
				t.Errorf("ResolvedTimeout(%q) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

func TestValidateDropsAnUnparseableAgentTimeout(t *testing.T) {
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
