package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredRuntimeServiceConfigNames(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		retired    bool
	}{
		{"old", "daemon:\n  idle_timeout: 0\n", true},
		{"new", "runtime_service:\n  idle_timeout: 0\n", false},
		{"unrelated", "agent:\n  args: [daemon]\n", false},
		{"merged", "old: &old\n  daemon: {}\n<<: *old\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			err := CheckRuntimeServiceConfigNames(path)
			if (err != nil) != tc.retired {
				t.Fatalf("retired=%v: %v", tc.retired, err)
			}
			if err != nil && !strings.Contains(err.Error(), "runtime_service") {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != tc.text {
				t.Fatal("validation rewrote config")
			}
		})
	}
}

func TestConfigWritesRejectRetiredHostKey(t *testing.T) {
	if err := validateMap(map[string]any{"daemon": map[string]any{"log_level": "info"}}); err == nil {
		t.Fatal("config.set can still write a retired host key")
	}
	if err := validateMap(map[string]any{"runtime_service": map[string]any{"log_level": "info"}}); err != nil {
		t.Fatal(err)
	}
}
