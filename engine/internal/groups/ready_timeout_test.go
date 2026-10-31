package groups

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An absent ready_timeout is the 30s default; a declared one is the service's
// own budget, and the published row carries it normalized.
func TestReadyTimeoutDefaultsAndPublishesEffectiveValue(t *testing.T) {
	cfg, err := loadString(t, "shop", `
name: shop
services:
  - name: api
    cmd: run api
    port: 8000
    ready_timeout: 90s
  - name: worker
    cmd: run worker
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Services[0].EffectiveReadyTimeout(); got != 90*time.Second {
		t.Errorf("api effective ready_timeout = %s, want 1m30s", got)
	}
	if got := cfg.Services[1].EffectiveReadyTimeout(); got != DefaultReadyTimeout {
		t.Errorf("worker effective ready_timeout = %s, want the %s default", got, DefaultReadyTimeout)
	}
	if got := ServiceRow(cfg.Services[0]).ReadyTimeout; got != "1m30s" {
		t.Errorf("published api ready_timeout = %q, want the normalized 1m30s", got)
	}
	if got := ServiceRow(cfg.Services[1]).ReadyTimeout; got != "30s" {
		t.Errorf("published worker ready_timeout = %q, want 30s", got)
	}
}

// UsesReadyTimeout is what lets the CLI refuse an old daemon: that daemon
// would ignore the declared budget and keep its fixed dependency wait. A file
// that only relies on the default must stay usable with it.
func TestUsesReadyTimeoutOnlyWhenDeclared(t *testing.T) {
	declared, err := loadString(t, "shop", `
name: shop
services:
  - name: api
    cmd: run api
    ready_timeout: 90s
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !declared.UsesReadyTimeout() {
		t.Error("UsesReadyTimeout = false, want true for a declared budget")
	}
	plain, err := loadString(t, "plain", `
name: plain
services:
  - name: api
    cmd: run api
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if plain.UsesReadyTimeout() {
		t.Error("UsesReadyTimeout = true, want false when only the default applies")
	}
}

// A value that is unparseable or asks for no time at all must fail at Load:
// otherwise a wait could expire before it started, or silently fall back while
// the file claims something else.
func TestReadyTimeoutRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{"not a duration", "soon", "is not a duration"},
		{"zero", "0s", "must be positive"},
		{"negative", "-5s", "must be positive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadString(t, "shop", `
name: shop
services:
  - name: api
    cmd: run api
    ready_timeout: `+tc.value+`
`)
			var bad *ConfigError
			if !errors.As(err, &bad) {
				t.Fatalf("Load error = %v, want a ConfigError", err)
			}
			if !strings.Contains(err.Error(), "service api: ready_timeout") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want it to name ready_timeout and %q", err, tc.want)
			}
		})
	}
}

// Every edit path re-parses the bytes it would write, so groups.config.set and
// groups.init reject a bad ready_timeout before touching the disk.
func TestReadyTimeoutValidationReachesEdits(t *testing.T) {
	dir := mkdir(t, tempTree(t), "shop")
	path := filepath.Join(dir, ConfigName)
	writeFile(t, path, "name: shop\nservices:\n  - name: api\n    cmd: run api\n    ready_timeout: 0s\n")
	// The rename keeps the bad service, so the re-parse must refuse it.
	if _, _, err := RenderEdit(path, ConfigEdit{Rename: []ServiceRename{{From: "api", To: "web"}}}); err == nil {
		t.Fatal("an edit kept an invalid ready_timeout")
	} else if !strings.Contains(err.Error(), "ready_timeout") {
		t.Fatalf("edit error = %v, want it to name ready_timeout", err)
	}
}
