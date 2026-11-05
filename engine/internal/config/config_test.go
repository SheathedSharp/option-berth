package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func writeUserConfig(t *testing.T, body string) {
	t.Helper()
	home := isolateHome(t)
	dir := filepath.Join(home, ".option-berth")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPathFollowsHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/config-fixture-home")
	want := filepath.Join("/tmp/config-fixture-home", ".option-berth", "config.yaml")
	if got := Path(); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestLoadWithoutAFileUsesDefaults(t *testing.T) {
	isolateHome(t)
	cfg, warnings := Load()
	if cfg == nil {
		t.Fatal("Load returned nil")
	}
	if len(warnings) != 0 {
		t.Errorf("a missing file warned: %v", warnings)
	}
	if cfg.Color != nil || len(cfg.Services) != 0 {
		t.Errorf("missing file yielded %+v, want an empty config", cfg)
	}
}

func TestLoadReadsServices(t *testing.T) {
	writeUserConfig(t, "services:\n  9000: php-fpm\n")
	cfg, warnings := Load()
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if cfg.Services[9000] != "php-fpm" {
		t.Errorf("services = %v", cfg.Services)
	}
}

func TestLoadReportsMalformedYAML(t *testing.T) {
	writeUserConfig(t, "daemon: [this is not: valid")
	cfg, warnings := Load()
	if cfg == nil {
		t.Fatal("Load returned nil")
	}
	if len(warnings) == 0 {
		t.Error("malformed YAML produced no warning")
	}
}

func TestLoadTreatsAnEmptyFileAsDefaults(t *testing.T) {
	writeUserConfig(t, "   \n")
	cfg, warnings := Load()
	if len(warnings) != 0 {
		t.Errorf("an empty file warned: %v", warnings)
	}
	if cfg.Color != nil || len(cfg.Services) != 0 {
		t.Error("an empty file did not yield an empty config")
	}
}

func TestLoadDropsOutOfRangeServicePorts(t *testing.T) {
	writeUserConfig(t, "services:\n  0: bad\n  70000: bad\n  8000: good\n")
	cfg, warnings := Load()
	if len(warnings) == 0 {
		t.Fatal("out-of-range ports produced no warning")
	}
	if _, present := cfg.Services[0]; present {
		t.Error("port 0 survived")
	}
	if _, present := cfg.Services[70000]; present {
		t.Error("port 70000 survived")
	}
	if cfg.Services[8000] != "good" {
		t.Error("the valid service was dropped")
	}
}

func TestWriteTemplateCreatesAUsableConfig(t *testing.T) {
	isolateHome(t)
	if err := WriteTemplate(false); err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("reading the template: %v", err)
	}
	if len(data) == 0 {
		t.Error("the template is empty")
	}
	cfg, warnings := Load()
	if len(warnings) != 0 {
		t.Errorf("the template warned: %v", warnings)
	}
	if cfg.Color != nil || len(cfg.Services) != 0 {
		t.Errorf("the template is not fully commented: %+v", cfg)
	}
}

func TestWriteTemplateWillNotClobberWithoutForce(t *testing.T) {
	isolateHome(t)
	if err := WriteTemplate(false); err != nil {
		t.Fatal(err)
	}
	if err := WriteTemplate(false); err == nil {
		t.Error("a second write without force succeeded")
	}
	if err := WriteTemplate(true); err != nil {
		t.Errorf("force did not overwrite: %v", err)
	}
}

// The template is the only documentation many users read, so it must mention
// every daemon setting and environment override the binary honours.
func TestTemplateMentionsDaemonAndEnvironment(t *testing.T) {
	anchors := []string{
		"# daemon:", "idle_timeout: 30m", "log_level: info",
		"stats_interval: 1s", "scan_interval: 2s",
		"BERTH_DB", "BERTH_SOCKET", "BERTH_STRICT_MANIFEST",
	}
	for _, anchor := range anchors {
		if !strings.Contains(template, anchor) {
			t.Errorf("template does not mention %q", anchor)
		}
	}
}

func TestTemplateMentionsTheAgentBlock(t *testing.T) {
	anchors := []string{"# agent:", "command: claude", "timeout: 20m", "args: []"}
	for _, anchor := range anchors {
		if !strings.Contains(template, anchor) {
			t.Errorf("template does not mention %q", anchor)
		}
	}
}

func TestResolvedStatsInterval(t *testing.T) {
	cases := []struct {
		label string
		raw   string
		want  time.Duration
	}{
		{"absent", "", DefaultStatsInterval},
		{"seconds", "2s", 2 * time.Second},
		{"sub-second", "500ms", 500 * time.Millisecond},
		{"below the floor", "10ms", MinStatsInterval},
		{"explicit zero", "0", DefaultStatsInterval},
		{"not a duration", "soon", DefaultStatsInterval},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := DaemonConfig{StatsInterval: tc.raw}.ResolvedStatsInterval()
			if got != tc.want {
				t.Errorf("ResolvedStatsInterval(%q) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

func TestValidateClampsStatsIntervalAndKeepsNeighbours(t *testing.T) {
	cfg := &Config{Daemon: DaemonConfig{StatsInterval: "5ms", LogLevel: "debug"}}
	warnings := validate(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "stats_interval") {
		t.Fatalf("warnings = %v, want one naming stats_interval", warnings)
	}
	if got := cfg.Daemon.ResolvedStatsInterval(); got != MinStatsInterval {
		t.Errorf("stats_interval = %s, want the floor %s", got, MinStatsInterval)
	}
	if cfg.Daemon.LogLevel != "debug" {
		t.Errorf("log_level = %q, a valid neighbour was dropped", cfg.Daemon.LogLevel)
	}
}

func TestResolvedScanInterval(t *testing.T) {
	cases := []struct {
		label string
		raw   string
		want  time.Duration
	}{
		{"absent", "", DefaultScanInterval},
		{"seconds", "5s", 5 * time.Second},
		{"at the floor", "1s", MinScanInterval},
		{"below the floor", "200ms", MinScanInterval},
		{"explicit zero", "0", DefaultScanInterval},
		{"not a duration", "whenever", DefaultScanInterval},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := DaemonConfig{ScanInterval: tc.raw}.ResolvedScanInterval()
			if got != tc.want {
				t.Errorf("ResolvedScanInterval(%q) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

func TestValidateClampsScanIntervalAndKeepsNeighbours(t *testing.T) {
	cfg := &Config{Daemon: DaemonConfig{ScanInterval: "50ms", StatsInterval: "1s"}}
	warnings := validate(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "scan_interval") {
		t.Fatalf("warnings = %v, want one naming scan_interval", warnings)
	}
	if got := cfg.Daemon.ResolvedScanInterval(); got != MinScanInterval {
		t.Errorf("scan_interval = %s, want the floor %s", got, MinScanInterval)
	}
	if cfg.Daemon.StatsInterval != "1s" {
		t.Errorf("stats_interval = %q, a valid neighbour was dropped", cfg.Daemon.StatsInterval)
	}
}

func TestValidateDropsUnparseableScanInterval(t *testing.T) {
	cfg := &Config{Daemon: DaemonConfig{ScanInterval: "every so often"}}
	warnings := validate(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "scan_interval") {
		t.Fatalf("warnings = %v, want one naming scan_interval", warnings)
	}
	if cfg.Daemon.ScanInterval != "" {
		t.Errorf("scan_interval = %q, want it dropped", cfg.Daemon.ScanInterval)
	}
	if got := cfg.Daemon.ResolvedScanInterval(); got != DefaultScanInterval {
		t.Errorf("scan_interval = %s, want the default %s", got, DefaultScanInterval)
	}
}
