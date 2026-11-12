package model

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLocalConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jev.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadLocalConfigValidatesAndTrims(t *testing.T) {
	path := writeLocalConfig(t, `{
      "schema":"oberth.jev-config/v1",
      "enabled":true,
      "provider":" OpenRouter ",
      "endpoint":"https://openrouter.example/decisions",
      "model":" typesafe/jev-1.13 ",
      "api_key":"  sk-test  ",
      "timeout_ms":12000
    }`, 0o600)
	cfg, present, err := LoadLocalConfigFrom(path)
	if err != nil || !present {
		t.Fatalf("load = %+v, present=%v, err=%v", cfg, present, err)
	}
	if !cfg.Enabled || cfg.Provider != "openrouter" || cfg.APIKey != "sk-test" {
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

	disabled := writeLocalConfig(t, `{"schema":"oberth.jev-config/v1","enabled":false}`, 0o600)
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

func TestReadLegacyJevKeyUsesOwnerOnlyFile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".option-berth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "decide.key"), []byte("legacy-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_HOME", dir)
	got, err := ReadLegacyJevKey()
	if err != nil || got != "legacy-key" {
		t.Fatalf("legacy key = %q err=%v", got, err)
	}
	if err := os.Chmod(filepath.Join(dir, "decide.key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLegacyJevKey(); err == nil {
		t.Fatal("broadly readable legacy key was accepted")
	}
}
