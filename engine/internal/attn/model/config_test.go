package model

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLocalConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "attn.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadLocalConfigValidatesAndTrims(t *testing.T) {
	path := writeLocalConfig(t, `{
      "schema":"oberth.attn-settings/v1",
      "enabled":true,
      "preset":" OpenRouter ",
      "endpoint":"https://openrouter.example/decisions",
      "model":" typesafe/jev-1.13 ",
      "api_key":"  sk-test  ",
      "timeout_ms":12000
    }`, 0o600)
	cfg, present, err := LoadLocalConfigFrom(path)
	if err != nil || !present {
		t.Fatalf("load = %+v, present=%v, err=%v", cfg, present, err)
	}
	if !cfg.Enabled || cfg.Preset != "openrouter" || cfg.APIKey != "sk-test" {
		t.Fatalf("normalized config = %+v", cfg)
	}
	if cfg.Timeout() != 12*time.Second {
		t.Fatalf("timeout = %s", cfg.Timeout())
	}
}

func TestLoadLocalConfigMissingIsDistinctFromDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	if _, present, err := LoadLocalConfigFrom(path); err != nil || present {
		t.Fatalf("missing = present=%v err=%v", present, err)
	}

	disabled := writeLocalConfig(t, `{"schema":"oberth.attn-settings/v1","enabled":false}`, 0o600)
	cfg, present, err := LoadLocalConfigFrom(disabled)
	if err != nil || !present || cfg.Enabled {
		t.Fatalf("disabled = %+v present=%v err=%v", cfg, present, err)
	}
}

func TestLoadLocalConfigRejectsBroadPermissions(t *testing.T) {
	path := writeLocalConfig(t, `{"enabled":true,"api_key":"secret"}`, 0o644)
	if _, present, err := LoadLocalConfigFrom(path); err == nil || !present {
		t.Fatalf("broad permissions accepted: present=%v err=%v", present, err)
	}
}
