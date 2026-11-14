package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"gopkg.in/yaml.v3"
)

// Config is the user's preferences file, ~/.option-berth/config.yaml.
type Config struct {
	RuntimeService RuntimeServiceConfig `yaml:"runtime_service"`

	// Color is a pointer so "unset" and an explicit false stay distinct.
	Color *bool `yaml:"color"`

	// Services labels custom or unknown ports, merged over the built-in
	// table: port number to display name.
	Services map[int]string `yaml:"services"`

	// Agent configures `oberth init draft`; see agent.go.
	Agent AgentConfig `yaml:"agent"`
}

// DefaultIdleTimeout is how long `oberth runtime service run` stays up with no clients, no
// subscribers and no keepalive connection.
const DefaultIdleTimeout = 30 * time.Minute

// The cadence bounds for `runtime_service.stats_interval` and `runtime_service.scan_interval`.
// These mirror the scanner's own constants; config does not import the
// scanner, which would drag the whole port pipeline into every CLI command
// that reads a config file.
const (
	// The stats tick: per-process cpu/memory plus the host load row.
	DefaultStatsInterval = 1 * time.Second
	MinStatsInterval     = 250 * time.Millisecond

	// The port-scan cadence and its floor.
	DefaultScanInterval = 2 * time.Second
	MinScanInterval     = 1 * time.Second
)

// RuntimeServiceConfig holds `oberth runtime service run` settings.
type RuntimeServiceConfig struct {
	// PreviousServiceLockDirs holds safety interlocks for explicitly selected
	// custom v2 endpoint directories. No socket in them is dialled or removed.
	PreviousServiceLockDirs []string `yaml:"previous_service_lock_dirs,omitempty"`

	// IdleTimeout is a Go duration ("30m", "2h"). "0" disables idle
	// shutdown; empty means DefaultIdleTimeout.
	IdleTimeout string `yaml:"idle_timeout"`

	// LogLevel is debug, info, warn or error. Empty means info.
	LogLevel string `yaml:"log_level"`

	// StatsInterval is how often per-process cpu/memory and the host load
	// row refresh while something is subscribed, as a Go duration ("1s",
	// "500ms"). Empty means DefaultStatsInterval; values below
	// MinStatsInterval are clamped. It never changes the port-scan cadence,
	// which is adaptive and owned by the scanner.
	StatsInterval string `yaml:"stats_interval"`

	// ScanInterval is the base cadence of the port scan, as a Go duration
	// ("2s", "5s"). Empty means DefaultScanInterval; values below
	// MinScanInterval are clamped. It sets the base only: the scanner still
	// backs off on unchanged scans, and both ceilings scale with this value,
	// so raising it slows the whole curve rather than only its fastest step.
	ScanInterval string `yaml:"scan_interval"`
}

// ResolvedIdleTimeout returns the parsed idle timeout, falling back to
// DefaultIdleTimeout when the setting is absent or unusable. Zero means
// "never shut down".
func (d RuntimeServiceConfig) ResolvedIdleTimeout() time.Duration {
	raw := strings.TrimSpace(d.IdleTimeout)
	if raw == "" {
		return DefaultIdleTimeout
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		return DefaultIdleTimeout
	}
	return value
}

// ResolvedStatsInterval returns the stats cadence, defaulting to
// DefaultStatsInterval and never below MinStatsInterval.
func (d RuntimeServiceConfig) ResolvedStatsInterval() time.Duration {
	value, ok := positiveDuration(d.StatsInterval)
	if !ok {
		// Including the unset case. There is no "off": the sampler parks
		// whenever nothing is subscribed, so zero would only be a busy loop.
		return DefaultStatsInterval
	}
	if value < MinStatsInterval {
		return MinStatsInterval
	}
	return value
}

// ResolvedScanInterval returns the base scan cadence, defaulting to
// DefaultScanInterval and never below MinScanInterval.
func (d RuntimeServiceConfig) ResolvedScanInterval() time.Duration {
	value, ok := positiveDuration(d.ScanInterval)
	if !ok {
		// Including the unset case. There is no "off": the loop parks
		// whenever nothing is subscribed and nothing is reading.
		return DefaultScanInterval
	}
	if value < MinScanInterval {
		return MinScanInterval
	}
	return value
}

// ResolvedLogLevel returns the configured level, defaulting to "info".
func (d RuntimeServiceConfig) ResolvedLogLevel() string {
	level := strings.ToLower(strings.TrimSpace(d.LogLevel))
	if !knownLogLevel(level) {
		return "info"
	}
	return level
}

func positiveDuration(raw string) (time.Duration, bool) {
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func knownLogLevel(level string) bool {
	switch level {
	case "debug", "info", "warn", "error":
		return true
	}
	return false
}

// Path returns the absolute config path. The directory layout is spelled out
// exactly once, in internal/paths.
func Path() string { return paths.ConfigPath() }

// Load reads and validates the config file. It never returns an error: a
// missing file yields an empty Config without warnings, while a malformed
// file or invalid values yield a Config with the bad settings dropped plus
// human-readable warnings for the caller to print.
func Load() (*Config, []string) { return LoadFrom(Path()) }

// LoadFrom is Load against an explicit file. `oberth doctor` uses it, because
// its checks read the config path from its own Env and going through Path()
// would read the developer's file instead of the fixture.
func LoadFrom(path string) (*Config, []string) {
	cfg := &Config{}

	data, err := os.ReadFile(path)
	if err != nil {
		// A missing or unreadable file is not an error: run on defaults.
		return cfg, nil
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return &Config{}, []string{fmt.Sprintf("ignoring %s: %v", path, err)}
	}
	return cfg, validate(cfg)
}

const template = `# option-berth configuration
#
# Every setting is optional. Uncomment a line to override the built-in
# default. Command-line flags always win over this file.

# runtime_service:
#   # Stop oberth runtime service run after this long without a client, a subscriber or
#   # a keepalive connection. 0 keeps it running until stopped by hand.
#   idle_timeout: 30m
#   # How much the runtime service writes to ~/.option-berth/runtime-service.log.
#   log_level: info     # debug | info | warn | error
#   # How often cpu, memory and the host load row refresh while something
#   # is watching. The floor is 250ms.
#   stats_interval: 1s
#   # Base cadence of the port scan. The scanner still backs off on an
#   # unchanged machine — to 2.5x this while a subscriber is connected and
#   # 5x when only RPC reads are served — so this moves the whole curve.
#   # The floor is 1s. Both cadences are read at startup: restart the
#   # runtime service after changing them; oberth runtime service status shows the values
#   # in effect.
#   scan_interval: 2s

# color: true       # false turns off colored output

# services:         # labels for custom or unknown ports
#   9000: php-fpm
#   5050: my-dashboard

# agent:            # how oberth init draft runs your own local agent
#                   # option-berth never talks to a model itself; a draft
#                   # becomes the manifest only when a person adopts it
#                   # (oberth init adopt).
#   command: claude # claude | codex. Empty auto-detects the one
#                   # supported CLI on PATH.
#   model: ""       # handed to the CLI (claude --model, codex -m). Empty
#                   # uses its default; names are not portable between CLIs.
#   timeout: 20m    # one drafting run. Minutes, not seconds: the agent
#                   # reads the project and may build and try services.
#   args: []        # extra CLI arguments before the prompt. This is where
#                   # a permission posture you chose goes — option-berth adds
#                   # none, so the run starts with the CLI's own defaults.
#                   # Examples:
#                   #   ["--allowedTools", "Bash"]              (claude)
#                   #   ["--sandbox", "workspace-write"]        (codex)

# Environment overrides (no config key; set them in your shell):
#   BERTH_DB       database for names, pins and history
#                  (default: option-berth.db beside this file)
#   BERTH_RUNTIME_SERVICE_SOCKET   socket the runtime service listens on and clients dial
#                  (default: what oberth runtime service path prints)
#   BERTH_RUNTIME_SERVICE_NO_AUTOSTART
#                  1 stops clients from starting a runtime service that is not
#                  already running; they report it as unavailable
#   BERTH_STRICT_MANIFEST
#                  1 makes an unknown oberth.yaml key a load error with its
#                  line and column instead of a warning
`

// WriteTemplate writes the commented starter config to Path(), creating the
// parent directory when needed. It refuses to overwrite unless force is true.
func WriteTemplate(force bool) error {
	path := Path()
	if err := ensureReplaceable(path, force); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return fmt.Errorf("could not write config: %w", err)
	}
	return nil
}

func ensureReplaceable(path string, force bool) error {
	if force {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", path)
	}
	return nil
}

// durationRule describes how one duration setting is repaired after parsing.
type durationRule struct {
	key      string
	fallback time.Duration
	// allowZero keeps an explicit "0" meaningful (idle shutdown off).
	allowZero bool
	// floor, when positive, is the minimum accepted value.
	floor time.Duration
}

// repairDuration drops or clamps one setting and appends the warning that
// explains what happened. Valid neighbouring settings are untouched.
func repairDuration(raw *string, rule durationRule, warnings *[]string) {
	value := strings.TrimSpace(*raw)
	if value == "" {
		return
	}
	parsed, err := time.ParseDuration(value)
	switch {
	case err != nil, parsed < 0, !rule.allowZero && parsed == 0:
		*warnings = append(*warnings, fmt.Sprintf(
			"config: invalid %s %q — using %s", rule.key, value, rule.fallback))
		*raw = ""
	case rule.floor > 0 && parsed < rule.floor:
		*warnings = append(*warnings, fmt.Sprintf(
			"config: %s %q is below the %s minimum — using %s",
			rule.key, value, rule.floor, rule.floor))
		*raw = rule.floor.String()
	}
}

// validate checks every setting and service port, dropping each invalid
// value and returning one warning per repair. `command` is deliberately not
// validated here: the supported set lives with the invocation matrix
// (internal/draft), and an unknown name is reported where the list can be
// shown — see cmd.resolveAgent.
func validate(cfg *Config) []string {
	var warnings []string

	repairDuration(&cfg.RuntimeService.IdleTimeout, durationRule{
		key: "runtime_service.idle_timeout", fallback: DefaultIdleTimeout, allowZero: true,
	}, &warnings)
	repairDuration(&cfg.RuntimeService.StatsInterval, durationRule{
		key: "runtime_service.stats_interval", fallback: DefaultStatsInterval, floor: MinStatsInterval,
	}, &warnings)
	repairDuration(&cfg.RuntimeService.ScanInterval, durationRule{
		key: "runtime_service.scan_interval", fallback: DefaultScanInterval, floor: MinScanInterval,
	}, &warnings)
	repairDuration(&cfg.Agent.Timeout, durationRule{
		key: "agent.timeout", fallback: DefaultAgentTimeout,
	}, &warnings)

	if level := strings.TrimSpace(cfg.RuntimeService.LogLevel); level != "" && !knownLogLevel(strings.ToLower(level)) {
		warnings = append(warnings, fmt.Sprintf("config: invalid runtime_service.log_level %q — using info", level))
		cfg.RuntimeService.LogLevel = ""
	}

	for port := range cfg.Services {
		if !validPort(port) {
			warnings = append(warnings, fmt.Sprintf("config: invalid service port %d — ignoring", port))
			delete(cfg.Services, port)
		}
	}
	return warnings
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}
