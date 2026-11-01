package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The optional assessment gets one row, and it is offline: no provider is
// contacted, so a slow or unreachable provider can never make a healthy
// installation look broken. The three states a person can act on are off,
// on-with-a-key, and on-without-one.

func writeJevSettings(t *testing.T, env *Env, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(env.Home, ".option-berth", "jev.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	env.SettingsPath = path
}

func TestJevCheckReportsOffWithoutFailing(t *testing.T) {
	env := fakeEnv(t)
	env.SettingsPath = filepath.Join(env.Home, ".option-berth", "jev.json")
	got := run(t, env, checkJev)
	// Skip, not fail: nothing is wrong, the feature is simply not enabled.
	wantStatus(t, got, StatusSkip)
	if got.Summary == "" || got.Fix == "" {
		t.Fatalf("check = %+v, want a summary and a next step", got)
	}
}

func TestJevCheckReportsOnWithProvider(t *testing.T) {
	env := fakeEnv(t)
	writeJevSettings(t, env, `{"schema":"oberth.jev-config/v1","enabled":true,"provider":"openrouter","api_key":"k"}`, 0o600)
	got := run(t, env, checkJev)
	wantStatus(t, got, StatusOK)
	if got.Summary != "Jev: on · openrouter" {
		t.Fatalf("summary = %q", got.Summary)
	}
	// The key must never appear in a doctor row.
	if strings.Contains(got.Detail, `"k"`) || strings.Contains(got.Summary, `"k"`) {
		t.Fatalf("the row leaks the key: %q / %q", got.Summary, got.Detail)
	}
}

func TestJevCheckWarnsWhenEnabledWithoutAKey(t *testing.T) {
	env := fakeEnv(t)
	writeJevSettings(t, env, `{"schema":"oberth.jev-config/v1","enabled":true,"provider":"typesafe"}`, 0o600)
	got := run(t, env, checkJev)
	wantStatus(t, got, StatusWarn)
	if got.Fix == "" {
		t.Fatalf("check = %+v, want a next step", got)
	}
}

// A malformed or world-readable settings file is the state a person cannot
// discover from the outside: it is present, so nothing falls back. This row is
// where it becomes visible.
func TestJevCheckWarnsOnUnusableSettings(t *testing.T) {
	env := fakeEnv(t)
	writeJevSettings(t, env, `{"schema":"oberth.jev-config/v1",`, 0o600)
	got := run(t, env, checkJev)
	wantStatus(t, got, StatusWarn)

	env2 := fakeEnv(t)
	writeJevSettings(t, env2, `{"schema":"oberth.jev-config/v1","enabled":true,"api_key":"k"}`, 0o644)
	got = run(t, env2, checkJev)
	wantStatus(t, got, StatusWarn)
}

func TestJevCheckIsInTheTableAndSelectable(t *testing.T) {
	found := false
	for _, id := range IDs() {
		if id == "jev" {
			found = true
		}
	}
	if !found {
		t.Fatalf("jev is not in the doctor table: %v", IDs())
	}
	if bad := UnknownSelectors([]string{"jev"}); len(bad) != 0 {
		t.Fatalf("--only jev is rejected: %v", bad)
	}
}
