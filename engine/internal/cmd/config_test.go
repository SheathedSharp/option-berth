package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigInitWritesThenRefusesThenForces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runConfigInit(false); err != nil {
		t.Fatalf("first runConfigInit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".option-berth", "config.yaml")); err != nil {
		t.Errorf("config file not created: %v", err)
	}
	if err := runConfigInit(false); err == nil {
		t.Error("a second run without force overwrote the file")
	}
	if err := runConfigInit(true); err != nil {
		t.Errorf("force did not overwrite: %v", err)
	}
}
