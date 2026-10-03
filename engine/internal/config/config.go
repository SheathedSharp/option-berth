package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/paths"
	"gopkg.in/yaml.v3"
)

// Config holds user preferences loaded from ~/.option-berth/config.yaml.
type Config struct {
	Daemon   DaemonConfig   `yaml:"daemon"`
	Color    *bool          `yaml:"color"`    // pointer: nil = unset, distinguishes from explicit false
	Services map[int]string `yaml:"services"` // port -> label, merged over built-in table
	// Share holds the sharing settings; today that is the relay this machine
	// signs in to (see share.go).
	Share ShareConfig `yaml:"share"`
	// Agent holds the drafting agent's settings (see agent.go): which local
	// CLI `oberth init draft` runs, and how long it gets.
	Agent AgentConfig `yaml:"agent"`
}

// DefaultIdleTimeout is how long `oberth serve` stays up with no clients, no
// subscribers and no keepalive connection.
const DefaultIdleTimeout = 30 * time.Minute

// DefaultStatsInterval and MinStatsInterval bound `daemon.stats_interval`, the
// cadence of the daemon's stats-only tick. DefaultScanInterval and
// MinScanInterval do the same for `daemon.scan_interval`, the base of the
// port-scan cadence. All four mirror the scanner's own constants;
// config does not import the scanner, which would drag the whole port
// pipeline into every CLI command that reads a config file.
const (
	DefaultStatsInterval = 1 * time.Second
	MinStatsInterval     = 250 * time.Millisecond
	DefaultScanInterval  = 2 * time.Second
	MinScanInterval      = 1 * time.Second
)

// DaemonConfig holds `oberth serve` settings.
type DaemonConfig struct {
	// IdleTimeout is a Go duration ("30m", "2h"). "0" disables idle shutdown;
	// empty means DefaultIdleTimeout.
	IdleTimeout string `yaml:"idle_timeout"`
	// LogLevel is debug, info, warn or error. Empty means info.
	LogLevel string `yaml:"log_level"`
	// StatsInterval is how often the daemon refreshes per-process cpu/memory
	// and the machine's own load row while something is subscribed, as a Go
	// duration ("1s", "500ms"). Empty means DefaultStatsInterval; anything
	// below MinStatsInterval is clamped to it. It does not affect how often
	// ports are scanned — that cadence is adaptive and owned by the scanner.
	StatsInterval string `yaml:"stats_interval"`
	// ScanInterval is the base cadence of the port scan, as a Go duration
	// ("2s", "5s"). Empty means DefaultScanInterval; anything below
	// MinScanInterval is clamped to it. It sets the *base*: the scanner still
	// backs off on unchanged scans, and the two ceilings it backs off to
	// scale with this value, so raising it makes the whole curve slower
	// rather than only its fastest step.
	ScanInterval string `yaml:"scan_interval"`
}

// ResolvedIdleTimeout returns the parsed idle timeout, falling back to
// DefaultIdleTimeout when the setting is absent. A zero return means "never".
func (d DaemonConfig) ResolvedIdleTimeout() time.Duration {
	if strings.TrimSpace(d.IdleTimeout) == "" {
		return DefaultIdleTimeout
	}
	v, err := time.ParseDuration(strings.TrimSpace(d.IdleTimeout))
	if err != nil || v < 0 {
		return DefaultIdleTimeout
	}
	return v
}

// ResolvedStatsInterval returns the parsed stats cadence, falling back to
// DefaultStatsInterval and never returning less than MinStatsInterval.
func (d DaemonConfig) ResolvedStatsInterval() time.Duration {
	v, err := time.ParseDuration(strings.TrimSpace(d.StatsInterval))
	if err != nil || v <= 0 {
		// Including the unset case. There is no "off": the sampler already
		// parks itself whenever nothing is subscribed, so a zero here would
		// only mean a busy loop.
		return DefaultStatsInterval
	}
	if v < MinStatsInterval {
		return MinStatsInterval
	}
	return v
}

// ResolvedScanInterval returns the parsed base scan cadence, falling back to
// DefaultScanInterval and never returning less than MinScanInterval.
func (d DaemonConfig) ResolvedScanInterval() time.Duration {
	v, err := time.ParseDuration(strings.TrimSpace(d.ScanInterval))
	if err != nil || v <= 0 {
		// Including the unset case. There is no "off": the loop already parks
		// itself whenever nothing is subscribed and nothing is reading.
		return DefaultScanInterval
	}
	if v < MinScanInterval {
		return MinScanInterval
	}
	return v
}

// ResolvedLogLevel returns the configured level, defaulting to "info".
func (d DaemonConfig) ResolvedLogLevel() string {
	level := strings.ToLower(strings.TrimSpace(d.LogLevel))
	if !validLogLevels[level] {
		return "info"
	}
	return level
}

// Path returns the absolute path to the config file. The layout itself lives in
// internal/paths — the directory is spelled out exactly once, there.
func Path() string { return paths.ConfigPath() }

// Load reads and validates the config file. It never returns an error: a
// missing file yields an empty Config with no warnings; a malformed file or
// invalid values yield a Config with the bad settings dropped plus
// human-readable warning strings for the caller to print.
func Load() (*Config, []string) { return LoadFrom(Path()) }

// LoadFrom is Load against an explicit file. `oberth doctor` uses it: its
// checks read the config path in its Env, which a test points at a fixture,
// and going through Path() would read the developer's own file instead.
func LoadFrom(path string) (*Config, []string) {
	cfg := &Config{}

	data, err := os.ReadFile(path)
	if err != nil {
		// Missing/unreadable file is not an error — run with defaults.
		return cfg, nil
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return &Config{}, []string{fmt.Sprintf("ignoring %s: %v", path, err)}
	}

	warnings := validate(cfg)
	return cfg, warnings
}

const template = `# option-berth configuration
# All settings are optional. Uncomment and edit to override built-in defaults.
# Explicit command-line flags always take precedence over this file.

# daemon:
#   # oberth serve stops after this long with no clients, no subscribers and
#   # no keepalive connection. 0 keeps it running until you stop it.
#   idle_timeout: 30m
#   # How much the daemon writes to ~/.option-berth/daemon.log.
#   log_level: info     # debug | info | warn | error
#   # How often cpu, memory and the host load strip refresh while the app (or
#   # any other subscriber) is watching. Minimum 250ms.
#   stats_interval: 1s
#   # The base cadence of the port scan. The scanner still slows itself down
#   # on unchanged scans — to 2.5x this while something is subscribed and 5x
#   # when only RPC reads are served — so this moves the whole curve.
#   # Minimum 1s. Both intervals are read at startup: restart the daemon to
#   # apply a change, and 'oberth daemon status' shows what is in effect.
#   scan_interval: 2s

# color: true       # set false to disable colored output

# services:         # label custom/unknown ports (port: name)
#   9000: php-fpm
#   5050: my-dashboard

# share:            # sharing a local port through the relay
#   # The relay option-berth signs in to and shares through. There is no
#   # default: unset means no relay at all; BERTH_RELAY overrides it for one
#   # process.
#   relay: https://relay.example

# agent:            # drafting a oberth.yaml with your own local agent
#                   # ('oberth init draft'). option-berth runs a CLI you
#                   # already have; it never talks to a model itself, and a
#                   # draft does not become the file until a person adopts it
#                   # ('oberth init adopt').
#   command: claude # claude | codex. Empty means auto-detect: the one
#                   # supported CLI on PATH.
#   model: ""       # optional: the model to hand the CLI (claude --model,
#                   # codex -m). Empty uses the CLI's own default. Names are
#                   # not portable between the two CLIs.
#   timeout: 20m    # one drafting run. Minutes, not seconds: the agent reads
#                   # the project, and may build it and try services.
#   args: []        # extra arguments for the agent CLI, before the prompt.
#                   # This is where a permission posture you chose goes —
#                   # option-berth adds none of its own, so the run starts with
#                   # the CLI's own defaults. For example:
#                   #   ["--allowedTools", "Bash"]              (claude)
#                   #   ["--sandbox", "workspace-write"]        (codex)

# Environment overrides (no config key; set them in your shell):
#   BERTH_DB       path to the database of names, pins and history
#                  (default: option-berth.db next to this file)
#   BERTH_SOCKET   path the daemon listens on and every client dials
#                  (default: what 'oberth daemon path' prints)
#   BERTH_NO_AUTOSTART
#                  set to 1 to stop clients starting a daemon that is not
#                  already running; they report it as unavailable instead
#   BERTH_RELAY    the relay to sign in to and share through, overriding
#                  share.relay
#   BERTH_CREDENTIALS_STORE
#                  set to 'file' to keep the relay session in
#                  ~/.option-berth/credentials.json instead of the OS keychain
`

// WriteTemplate writes a commented starter config to Path(), creating the
// parent directory if needed. It refuses to overwrite an existing file unless
// force is true.
func WriteTemplate(force bool) error {
	path := Path()
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("config already exists at %s (use --force to overwrite)", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return fmt.Errorf("could not write config: %w", err)
	}
	return nil
}

var validLogLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

// validate checks settings and service ports, dropping any invalid value
// and returning a warning for each. Valid neighboring settings are preserved.
func validate(cfg *Config) []string {
	var warnings []string

	// The relay is validated here rather than in Relay() so that
	// `oberth config set share.relay …` refuses a bad value at the moment it
	// is written, instead of accepting it and ignoring it on every load.
	if _, w := ResolveRelay("", cfg.Share.Relay); w != "" {
		warnings = append(warnings, w)
		cfg.Share.Relay = ""
	}

	if v := strings.TrimSpace(cfg.Daemon.IdleTimeout); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d < 0 {
			warnings = append(warnings, fmt.Sprintf("config: invalid daemon.idle_timeout %q — using %s", v, DefaultIdleTimeout))
			cfg.Daemon.IdleTimeout = ""
		}
	}

	if v := strings.TrimSpace(cfg.Daemon.StatsInterval); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			warnings = append(warnings, fmt.Sprintf("config: invalid daemon.stats_interval %q — using %s", v, DefaultStatsInterval))
			cfg.Daemon.StatsInterval = ""
		} else if d < MinStatsInterval {
			warnings = append(warnings, fmt.Sprintf("config: daemon.stats_interval %q is below the %s minimum — using %s", v, MinStatsInterval, MinStatsInterval))
			cfg.Daemon.StatsInterval = MinStatsInterval.String()
		}
	}

	if v := strings.TrimSpace(cfg.Daemon.ScanInterval); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			warnings = append(warnings, fmt.Sprintf("config: invalid daemon.scan_interval %q — using %s", v, DefaultScanInterval))
			cfg.Daemon.ScanInterval = ""
		} else if d < MinScanInterval {
			warnings = append(warnings, fmt.Sprintf("config: daemon.scan_interval %q is below the %s minimum — using %s", v, MinScanInterval, MinScanInterval))
			cfg.Daemon.ScanInterval = MinScanInterval.String()
		}
	}

	if v := strings.TrimSpace(cfg.Daemon.LogLevel); v != "" && !validLogLevels[strings.ToLower(v)] {
		warnings = append(warnings, fmt.Sprintf("config: invalid daemon.log_level %q — using info", v))
		cfg.Daemon.LogLevel = ""
	}

	// The agent block. `command` is deliberately not validated here: the
	// supported set lives with the invocation matrix (internal/draft), and an
	// unknown name is reported where the list can come with it — see
	// cmd.resolveAgent. A bad timeout is the same dropped-and-warned shape as
	// every other duration.
	if v := strings.TrimSpace(cfg.Agent.Timeout); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			warnings = append(warnings, fmt.Sprintf("config: invalid agent.timeout %q — using %s", v, DefaultAgentTimeout))
			cfg.Agent.Timeout = ""
		}
	}

	for port := range cfg.Services {
		if port < 1 || port > 65535 {
			warnings = append(warnings, fmt.Sprintf("config: invalid service port %d — ignoring", port))
			delete(cfg.Services, port)
		}
	}

	return warnings
}
