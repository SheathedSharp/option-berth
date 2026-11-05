package spawn

import (
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// TestChildEnvCarriesTheRunIdentity is the child-side half of "every run is
// attributable": a tool inside the child reads BERTH_* and learns which run it
// belongs to, whatever the caller's own environment said.
func TestChildEnvCarriesTheRunIdentity(t *testing.T) {
	environment := childEnv(Request{
		Env:      []string{"PATH=/bin", "BERTH_GROUP=stale", "BERTH_RUN_ID=stale"},
		Group:    "option-berth",
		Name:     "api",
		PortHint: 8000,
	}, "abc123")

	got := parseEnv(t, environment)
	want := map[string]string{
		"PATH":      "/bin",
		EnvGroup:    "option-berth",
		EnvName:     "api",
		EnvRunID:    "abc123",
		EnvPortHint: "8000",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
}

// TestChildEnvDropsAnotherRunsIdentity: the caller's environment may already
// carry BERTH_* from a run this one is nested inside. Inheriting it would make
// the child report the outer run's group, so every run variable is replaced
// rather than appended alongside.
func TestChildEnvDropsAnotherRunsIdentity(t *testing.T) {
	environment := childEnv(Request{
		Env:   []string{"BERTH_NAME=outer", "BERTH_PORT=1", "KEEP=yes"},
		Group: "itest",
		Name:  "inner",
	}, "id")
	got := parseEnv(t, environment)

	if got["KEEP"] != "yes" {
		t.Errorf("an unrelated variable was dropped: %q", environment)
	}
	if got[EnvName] != "inner" {
		t.Errorf("%s = %q, want inner", EnvName, got[EnvName])
	}
	// No port hint on this request, so the inherited one must not survive.
	if _, present := got[EnvPortHint]; present {
		t.Errorf("%s survived from the outer run: %q", EnvPortHint, environment)
	}
	if strings.Contains(strings.Join(environment, " "), "BERTH_PORT") {
		t.Errorf("a stale %s is still in the environment", EnvPortHint)
	}
}

// TestChildEnvCarriesTheSessionOnlyWhenThereIsOne: a run without a session must
// not be handed a half-populated BERTH_SESSION, and one with a session must be
// readable in the `<tool>:<id>` form a nested start reads back.
func TestChildEnvCarriesTheSession(t *testing.T) {
	without := parseEnv(t, childEnv(Request{Env: []string{}, Session: state.Session{Tool: "claude"}}, "id"))
	if _, present := without[EnvSession]; present {
		t.Errorf("a session with no id was still exported: %q", without[EnvSession])
	}

	with := parseEnv(t, childEnv(Request{
		Env: []string{},
		Session: state.Session{
			ID: "sess-1", Tool: "claude", Label: "reviewing the spawn rewrite",
		},
	}, "id"))
	if with[EnvSession] != "claude:sess-1" {
		t.Errorf("%s = %q, want claude:sess-1", EnvSession, with[EnvSession])
	}
	if with[EnvSessionLabel] != "reviewing the spawn rewrite" {
		t.Errorf("%s = %q", EnvSessionLabel, with[EnvSessionLabel])
	}
}

// parseEnv turns an environment into a map and fails on a duplicate key: a
// variable defined twice is the very bug these tests exist to catch.
func parseEnv(t *testing.T, environment []string) map[string]string {
	t.Helper()
	parsed := make(map[string]string, len(environment))
	for _, pair := range environment {
		key, value, _ := strings.Cut(pair, "=")
		if _, duplicate := parsed[key]; duplicate {
			t.Fatalf("%s appears twice in %q", key, environment)
		}
		parsed[key] = value
	}
	return parsed
}
