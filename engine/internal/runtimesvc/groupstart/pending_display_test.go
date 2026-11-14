package groupstart

import "testing"

func TestDependencyPendingDisplayKeepsNoPortDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		port         bool
		want         string
	}{
		{"worker", "not_running", false, "worker to be running (no port)"},
		{"api", "not_running", true, "api (not_running)"},
		{"api", "waiting_for_listener", true, "api (waiting_for_listener)"},
		{"worker", "waiting_for_health", false, "worker (waiting_for_health)"},
		{"worker", "health_failed", false, "worker (health_failed)"},
	} {
		t.Run(tc.name+"/"+tc.reason, func(t *testing.T) {
			if got := dependencyPendingDisplay(tc.name, tc.reason, tc.port); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
