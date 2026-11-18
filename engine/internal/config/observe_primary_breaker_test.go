package config

import (
	"strings"
	"testing"
	"time"
)

// A file that says nothing gets the conservative defaults: three failures in a
// row before the primary stops being tried, and a minute between probes.
func TestPrimaryBreakerDefaultsAreConservative(t *testing.T) {
	isolateHome(t)
	cfg, warnings := Load()
	if len(warnings) != 0 {
		t.Fatalf("a missing file warned: %v", warnings)
	}
	if got := cfg.RuntimeService.ResolvedObservePrimaryFailures(); got != DefaultObservePrimaryFailures {
		t.Fatalf("observe_primary_failures = %d, want %d", got, DefaultObservePrimaryFailures)
	}
	if got := cfg.RuntimeService.ResolvedObservePrimaryCooldown(); got != DefaultObservePrimaryCooldown {
		t.Fatalf("observe_primary_cooldown = %s, want %s", got, DefaultObservePrimaryCooldown)
	}
	if DefaultObservePrimaryFailures != 3 || DefaultObservePrimaryCooldown != 60*time.Second {
		t.Fatalf("defaults = %d/%s, want the documented 3/60s",
			DefaultObservePrimaryFailures, DefaultObservePrimaryCooldown)
	}
}

// Both numbers are the user's to choose: a machine whose primary is slow to
// recover can wait longer between probes, and one that expects it to come back
// quickly can shorten the window.
func TestLoadKeepsConfiguredPrimaryBreaker(t *testing.T) {
	writeUserConfig(t, "runtime_service:\n  observe_primary_failures: 5\n  observe_primary_cooldown: 5m\n")
	cfg, warnings := Load()
	if len(warnings) != 0 {
		t.Fatalf("a valid breaker warned: %v", warnings)
	}
	if got := cfg.RuntimeService.ResolvedObservePrimaryFailures(); got != 5 {
		t.Fatalf("observe_primary_failures = %d, want 5", got)
	}
	if got := cfg.RuntimeService.ResolvedObservePrimaryCooldown(); got != 5*time.Minute {
		t.Fatalf("observe_primary_cooldown = %s, want 5m", got)
	}
}

// Zero is a decision, not a typo: it turns the breaker off, so every scan pays
// for the primary exactly as it did before the breaker existed.
func TestZeroFailuresTurnsTheBreakerOff(t *testing.T) {
	writeUserConfig(t, "runtime_service:\n  observe_primary_failures: 0\n")
	cfg, warnings := Load()
	if len(warnings) != 0 {
		t.Fatalf("turning the breaker off warned: %v", warnings)
	}
	if got := cfg.RuntimeService.ResolvedObservePrimaryFailures(); got != 0 {
		t.Fatalf("observe_primary_failures = %d, want 0 (off)", got)
	}
}

// A count or a cooldown this build cannot use is repaired once, and the warning
// names the setting and what the runtime service will do instead.
func TestLoadRepairsAnUnusablePrimaryBreaker(t *testing.T) {
	t.Run("negative failures", func(t *testing.T) {
		writeUserConfig(t, "runtime_service:\n  observe_primary_failures: -2\n")
		cfg, warnings := Load()
		if len(warnings) != 1 || !strings.Contains(warnings[0], "runtime_service.observe_primary_failures") ||
			!strings.Contains(warnings[0], "using 3") {
			t.Fatalf("warnings = %v, want one naming the setting and the default", warnings)
		}
		if got := cfg.RuntimeService.ResolvedObservePrimaryFailures(); got != DefaultObservePrimaryFailures {
			t.Fatalf("observe_primary_failures = %d, want %d", got, DefaultObservePrimaryFailures)
		}
	})
	t.Run("unusable cooldown", func(t *testing.T) {
		for _, value := range []string{"0s", "-1m", "soon"} {
			writeUserConfig(t, "runtime_service:\n  observe_primary_cooldown: \""+value+"\"\n")
			cfg, warnings := Load()
			if len(warnings) != 1 || !strings.Contains(warnings[0], "runtime_service.observe_primary_cooldown") ||
				!strings.Contains(warnings[0], "using 1m0s") {
				t.Fatalf("%s: warnings = %v, want one naming the setting and the default", value, warnings)
			}
			if got := cfg.RuntimeService.ResolvedObservePrimaryCooldown(); got != DefaultObservePrimaryCooldown {
				t.Fatalf("%s: observe_primary_cooldown = %s, want %s", value, got, DefaultObservePrimaryCooldown)
			}
		}
	})
}

// `oberth config set` runs the same validation, so a number this build would
// repair is refused rather than written and warned about on every read.
func TestApplyRefusesAnUnusablePrimaryBreaker(t *testing.T) {
	configDir(t)
	if _, err := Apply(map[string]any{"runtime_service": map[string]any{"observe_primary_failures": -1}}); err == nil {
		t.Fatal("Apply accepted observe_primary_failures -1")
	}
	if _, err := Apply(map[string]any{"runtime_service": map[string]any{"observe_primary_cooldown": "0s"}}); err == nil {
		t.Fatal("Apply accepted a zero cooldown")
	}
	if _, err := Apply(map[string]any{"runtime_service": map[string]any{"observe_primary_failures": 0}}); err != nil {
		t.Fatalf("Apply refused turning the breaker off: %v", err)
	}
	if _, err := Apply(map[string]any{"runtime_service": map[string]any{"observe_primary_failures": 4}}); err != nil {
		t.Fatalf("Apply refused a usable count: %v", err)
	}
}
