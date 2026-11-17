package config

import (
	"strings"
	"testing"
)

// One collector is the default in both senses: a file that does not mention the
// key, and a Config built without it, read the primary table alone.
func TestObserveSourcesDefaultsToOne(t *testing.T) {
	isolateHome(t)
	cfg, warnings := Load()
	if len(warnings) != 0 {
		t.Fatalf("a missing file warned: %v", warnings)
	}
	if got := cfg.RuntimeService.ResolvedObserveSources(); got != DefaultObserveSources {
		t.Fatalf("observe_sources = %d, want the default %d", got, DefaultObserveSources)
	}
	if got := (RuntimeServiceConfig{}).ResolvedObserveSources(); got != DefaultObserveSources {
		t.Fatalf("zero value resolved to %d, want %d", got, DefaultObserveSources)
	}
}

// A count the user stated is kept: merging is opt-in, and the number is theirs
// to choose.
func TestLoadKeepsConfiguredObserveSources(t *testing.T) {
	writeUserConfig(t, "runtime_service:\n  observe_sources: 2\n")
	cfg, warnings := Load()
	if len(warnings) != 0 {
		t.Fatalf("a valid count warned: %v", warnings)
	}
	if got := cfg.RuntimeService.ResolvedObserveSources(); got != 2 {
		t.Fatalf("observe_sources = %d, want 2", got)
	}
}

// A count that cannot be read as "at least one table" is repaired once, and the
// warning says what the runtime service will do instead.
func TestLoadRepairsAnUnusableObserveSources(t *testing.T) {
	for _, raw := range []string{"0", "-3"} {
		t.Run(raw, func(t *testing.T) {
			writeUserConfig(t, "runtime_service:\n  observe_sources: "+raw+"\n")
			cfg, warnings := Load()
			if len(warnings) != 1 || !strings.Contains(warnings[0], "runtime_service.observe_sources") ||
				!strings.Contains(warnings[0], "using 1") {
				t.Fatalf("warnings = %v, want one naming the setting and the default", warnings)
			}
			if got := cfg.RuntimeService.ResolvedObserveSources(); got != DefaultObserveSources {
				t.Fatalf("observe_sources = %d, want %d", got, DefaultObserveSources)
			}
		})
	}
}

// `oberth config set` runs the same validation, so an unusable count is refused
// rather than written and repaired on the next read.
func TestApplyRefusesAnUnusableObserveSources(t *testing.T) {
	configDir(t)
	if _, err := Apply(map[string]any{"runtime_service": map[string]any{"observe_sources": 0}}); err == nil {
		t.Fatal("Apply accepted observe_sources 0")
	}
	if _, err := Apply(map[string]any{"runtime_service": map[string]any{"observe_sources": 3}}); err != nil {
		t.Fatalf("Apply refused a usable count: %v", err)
	}
}
