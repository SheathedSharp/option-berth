package config

import (
	"os"
	"path/filepath"
	"testing"
)

// tempHome points Path() at a scratch directory for the duration of a test.
func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return filepath.Join(home, ".option-berth")
}

func TestMapOnAMissingFileIsEmpty(t *testing.T) {
	tempHome(t)
	m, err := Map()
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("Map = %v, want empty", m)
	}
}

func TestApplyWritesTheFileAndKeepsABackup(t *testing.T) {
	dir := tempHome(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("daemon:\n  log_level: info\n  idle_timeout: 5m\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Apply(map[string]any{"daemon": map[string]any{"log_level": "debug"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	daemon, _ := got["daemon"].(map[string]any)
	if daemon["log_level"] != "debug" {
		t.Errorf("daemon.log_level = %v, want debug", daemon["log_level"])
	}
	if daemon["idle_timeout"] != "5m" {
		t.Errorf("daemon.idle_timeout = %v, want 5m: a patch merges, it does not replace", daemon["idle_timeout"])
	}

	// The file on disk agrees with what was returned.
	reread, err := Map()
	if err != nil {
		t.Fatal(err)
	}
	if reread["daemon"].(map[string]any)["log_level"] != "debug" {
		t.Errorf("reread config = %v, want daemon.log_level debug", reread)
	}

	bak, err := os.ReadFile(BackupPath())
	if err != nil {
		t.Fatalf("reading the backup: %v", err)
	}
	if string(bak) != "daemon:\n  log_level: info\n  idle_timeout: 5m\n" {
		t.Errorf("backup = %q, want the file as it was before the write", bak)
	}
}

func TestApplyClearsAKeyWithNull(t *testing.T) {
	tempHome(t)
	if _, err := Apply(map[string]any{"color": false}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := Apply(map[string]any{"color": nil})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, ok := got["color"]; ok {
		t.Errorf("color survived a null patch: %v", got)
	}
}

func TestApplyRejectsAnInvalidValueAndLeavesTheFileAlone(t *testing.T) {
	tempHome(t)
	if _, err := Apply(map[string]any{"daemon": map[string]any{"log_level": "warn"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(map[string]any{"daemon": map[string]any{"log_level": "sideways"}}); err == nil {
		t.Fatal("Apply accepted an invalid log level")
	}
	cfg, _ := Load()
	if cfg.Daemon.LogLevel != "warn" {
		t.Errorf("daemon.log_level = %q, want the rejected write to have changed nothing", cfg.Daemon.LogLevel)
	}
}

func TestNumericKeysSurviveARoundTrip(t *testing.T) {
	tempHome(t)
	got, err := Apply(map[string]any{"services": map[string]any{"9000": "php-fpm"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got["services"].(map[string]any)["9000"] != "php-fpm" {
		t.Errorf("services = %v, want the port keyed as a string on the wire", got["services"])
	}
	cfg, warnings := Load()
	if len(warnings) > 0 {
		t.Errorf("re-reading the written config warned: %v", warnings)
	}
	if cfg.Services[9000] != "php-fpm" {
		t.Errorf("services = %v, want 9000 -> php-fpm parsed back as an int key", cfg.Services)
	}
}

func TestUnknownKeysSurviveAWrite(t *testing.T) {
	dir := tempHome(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("remote:\n  hosts: [me@box]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Apply(map[string]any{"color": true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	remote, ok := got["remote"].(map[string]any)
	if !ok || len(remote["hosts"].([]any)) != 1 {
		t.Errorf("config = %v, want the unknown remote.hosts key preserved", got)
	}
}
