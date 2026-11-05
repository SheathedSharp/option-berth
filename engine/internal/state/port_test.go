package state

import (
	"encoding/json"
	"testing"
)

func TestPortKeyCombinesPortAndBind(t *testing.T) {
	row := Port{Port: 3000, BindAddress: "127.0.0.1"}
	if got := row.Key(); got != "3000:127.0.0.1" {
		t.Fatalf("Key() = %q", got)
	}
}

func TestPortJSONEncodesAbsentOptionalsAsNull(t *testing.T) {
	encoded, err := json.Marshal(Port{Port: 3000, BindAddress: "::", Type: TypeUser})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	optional := []string{
		"name", "project_root", "group", "group_source", "session",
		"run", "stats", "health", "docker", "started_at",
	}
	for _, key := range optional {
		value, present := fields[key]
		if !present {
			t.Fatalf("%q is missing", key)
		}
		if value != nil {
			t.Fatalf("%q = %v, want null", key, value)
		}
	}
	// These share/proxy fields were removed; nothing may bring them back.
	for _, removed := range []string{"shares", "exposed_urls", "proxy_id", "proxy_target_port"} {
		if _, present := fields[removed]; present {
			t.Fatalf("removed key %q still marshals", removed)
		}
	}
	if fields["type"] != "user" {
		t.Fatalf("type = %v", fields["type"])
	}
}

// The published vocabulary has exactly three words; the probe's own word stays
// as the reason so a client can explain a failure without branching on it.
func TestNormalizeHealthVocabulary(t *testing.T) {
	cases := []struct {
		raw    string
		status string
		reason string
	}{
		// Successes.
		{"healthy", HealthOK, "healthy"},
		{"ok", HealthOK, "ok"},

		// Failures.
		{"unhealthy", HealthFail, "unhealthy"},
		{"refused", HealthFail, "refused"},
		{"timeout", HealthFail, "timeout"},
		{"non-http", HealthFail, "non-http"},

		// Already-normalized and empty input.
		{"fail", HealthFail, ""},
		{"unknown", HealthUnknown, ""},
		{"", HealthUnknown, ""},
	}
	for _, tc := range cases {
		status, reason := NormalizeHealth(tc.raw)
		if status != tc.status || reason != tc.reason {
			t.Errorf("NormalizeHealth(%q) = %q, %q; want %q, %q",
				tc.raw, status, reason, tc.status, tc.reason)
		}
	}
}
