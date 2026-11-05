package config

import (
	"os"
	"path/filepath"
	"testing"
)

func configDir(t *testing.T) string {
	t.Helper()
	home := isolateHome(t)
	dir := filepath.Join(home, ".option-berth")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustMap(t *testing.T) map[string]any {
	t.Helper()
	m, err := Map()
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	return m
}

func TestMapOfAMissingFileIsEmpty(t *testing.T) {
	isolateHome(t)
	if m := mustMap(t); len(m) != 0 {
		t.Errorf("Map = %v, want empty", m)
	}
}

func TestApplyMergesKeysAndKeepsThePreviousFile(t *testing.T) {
	configDir(t)
	original := "daemon:\n  log_level: info\n  idle_timeout: 5m\n"
	if err := os.WriteFile(Path(), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	merged, err := Apply(map[string]any{"daemon": map[string]any{"log_level": "debug"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	daemon, _ := merged["daemon"].(map[string]any)
	if daemon["log_level"] != "debug" {
		t.Errorf("daemon.log_level = %v, want debug", daemon["log_level"])
	}
	if daemon["idle_timeout"] != "5m" {
		t.Errorf("daemon.idle_timeout = %v, want 5m: patches merge, they do not replace", daemon["idle_timeout"])
	}

	reread, err := Map()
	if err != nil {
		t.Fatal(err)
	}
	if reread["daemon"].(map[string]any)["log_level"] != "debug" {
		t.Errorf("reread config = %v", reread)
	}

	backup, err := os.ReadFile(BackupPath())
	if err != nil {
		t.Fatalf("reading the backup: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want the file as it was", backup)
	}
}

func TestApplyNullRemovesAKey(t *testing.T) {
	configDir(t)
	if _, err := Apply(map[string]any{"color": false}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	merged, err := Apply(map[string]any{"color": nil})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, present := merged["color"]; present {
		t.Errorf("color survived a null patch: %v", merged)
	}
}

func TestApplyRejectsAnInvalidValueWithoutWriting(t *testing.T) {
	configDir(t)
	if _, err := Apply(map[string]any{"daemon": map[string]any{"log_level": "warn"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(map[string]any{"daemon": map[string]any{"log_level": "sideways"}}); err == nil {
		t.Fatal("Apply accepted an invalid log level")
	}
	cfg, _ := Load()
	if cfg.Daemon.LogLevel != "warn" {
		t.Errorf("daemon.log_level = %q, want the rejected write to change nothing", cfg.Daemon.LogLevel)
	}
}

func TestNumericServiceKeysSurviveARoundTrip(t *testing.T) {
	configDir(t)
	merged, err := Apply(map[string]any{"services": map[string]any{"9000": "php-fpm"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if merged["services"].(map[string]any)["9000"] != "php-fpm" {
		t.Errorf("services = %v, want the string key on the wire", merged["services"])
	}

	cfg, warnings := Load()
	if len(warnings) > 0 {
		t.Errorf("re-reading the written config warned: %v", warnings)
	}
	if cfg.Services[9000] != "php-fpm" {
		t.Errorf("services = %v, want 9000 parsed back as a port", cfg.Services)
	}
}

func TestUnknownKeysSurviveAWrite(t *testing.T) {
	configDir(t)
	if err := os.WriteFile(Path(), []byte("remote:\n  hosts: [me@box]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	merged, err := Apply(map[string]any{"color": true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	remote, ok := merged["remote"].(map[string]any)
	if !ok || len(remote["hosts"].([]any)) != 1 {
		t.Errorf("config = %v, want remote.hosts preserved", merged)
	}
}
