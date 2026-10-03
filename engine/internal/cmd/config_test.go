package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigInitWritesTemplate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runConfigInit(false); err != nil {
		t.Fatalf("runConfigInit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".option-berth", "config.yaml")); err != nil {
		t.Errorf("config file not created: %v", err)
	}
	// Second call without force must fail.
	if err := runConfigInit(false); err == nil {
		t.Error("expected error on overwrite without force")
	}
	// With force it succeeds.
	if err := runConfigInit(true); err != nil {
		t.Errorf("force should overwrite: %v", err)
	}
}
