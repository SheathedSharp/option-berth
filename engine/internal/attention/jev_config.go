package attention

// Local Jev configuration is intentionally owned by the optional adapter and
// the desktop settings page. Keeping it outside config.yaml means generic
// `oberth config get` output can never echo an API key, while the agent-side
// runner can still find the setting without a daemon or a shell profile.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/paths"
)

const (
	LocalJevConfigSchema = "oberth.jev-config/v1"
	maxLocalJevConfig    = 64 * 1024
	maxLocalJevKey       = 4096
	minLocalJevTimeout   = 250 * time.Millisecond
	maxLocalJevTimeout   = 2 * time.Minute
)

// LocalJevConfig is the on-disk shape written by the macOS settings page.
// APIKey is never included in telemetry, attention artifacts, or diagnostic
// output. The file itself is required to be owner-only on Unix systems.
type LocalJevConfig struct {
	Schema    string `json:"schema"`
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	APIKey    string `json:"api_key"`
	TimeoutMS int    `json:"timeout_ms"`
}

// LocalJevConfigPath returns the path shared by the GUI and the adapter.
func LocalJevConfigPath() string { return paths.JevConfigPath() }

// LoadLocalJevConfig reads the local settings file. present distinguishes a
// missing file from an explicitly disabled file, which is important: once a
// user turns Jev off, the legacy decide.key must not silently re-enable it.
func LoadLocalJevConfig() (cfg LocalJevConfig, present bool, err error) {
	return LoadLocalJevConfigFrom(paths.JevConfigPath())
}

// LoadLocalJevConfigFrom is injectable for tests and isolated acceptance runs.
func LoadLocalJevConfigFrom(path string) (cfg LocalJevConfig, present bool, err error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return LocalJevConfig{}, false, nil
		}
		return LocalJevConfig{}, true, fmt.Errorf("reading Jev settings: %w", statErr)
	}
	if !info.Mode().IsRegular() {
		return LocalJevConfig{}, true, errors.New("Jev settings are not a regular file")
	}
	if info.Size() > maxLocalJevConfig {
		return LocalJevConfig{}, true, errors.New("Jev settings file is too large")
	}
	if err := ownerOnly(path, info.Mode().Perm()); err != nil {
		return LocalJevConfig{}, true, err
	}
	file, err := os.Open(path)
	if err != nil {
		return LocalJevConfig{}, true, fmt.Errorf("reading Jev settings: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxLocalJevConfig))
	if err := decoder.Decode(&cfg); err != nil {
		return LocalJevConfig{}, true, errors.New("Jev settings are not valid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return LocalJevConfig{}, true, errors.New("Jev settings contain more than one JSON value")
	}
	if err := cfg.Validate(); err != nil {
		return LocalJevConfig{}, true, err
	}
	return cfg, true, nil
}

// Validate checks the small, stable settings surface. It intentionally does
// not validate the remote model's existence; a network/provider error belongs
// to the adapter's ordinary unavailable path.
func (c *LocalJevConfig) Validate() error {
	if c.Schema != "" && c.Schema != LocalJevConfigSchema {
		return errors.New("Jev settings schema is unsupported")
	}
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = "openrouter"
	}
	if c.Provider != "openrouter" && c.Provider != "typesafe" {
		return errors.New("Jev settings provider is unsupported")
	}
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("Jev settings endpoint must be an HTTPS URL")
		}
	}
	c.Model = strings.TrimSpace(c.Model)
	c.APIKey = strings.TrimSpace(c.APIKey)
	if len(c.APIKey) > maxLocalJevKey {
		return errors.New("Jev settings API key is too long")
	}
	if c.TimeoutMS != 0 {
		timeout := time.Duration(c.TimeoutMS) * time.Millisecond
		if timeout < minLocalJevTimeout || timeout > maxLocalJevTimeout {
			return errors.New("Jev settings timeout is outside the supported range")
		}
	}
	return nil
}

// Timeout returns the configured timeout or the adapter default.
func (c LocalJevConfig) Timeout() time.Duration {
	if c.TimeoutMS == 0 {
		return DefaultJevTimeout
	}
	timeout := time.Duration(c.TimeoutMS) * time.Millisecond
	if timeout < minLocalJevTimeout || timeout > maxLocalJevTimeout {
		return DefaultJevTimeout
	}
	return timeout
}

// ReadLegacyJevKey retains compatibility with the first adapter prototype.
// It is only consulted when jev.json is absent; an explicit local settings
// file, including enabled=false, always wins.
func ReadLegacyJevKey() (string, error) {
	path := paths.LegacyJevKeyPath()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading legacy Jev key: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("legacy Jev key is not a regular file")
	}
	if err := ownerOnly(path, info.Mode().Perm()); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("reading legacy Jev key: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxLocalJevKey+1))
	if err != nil {
		return "", fmt.Errorf("reading legacy Jev key: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if len(key) > maxLocalJevKey {
		return "", errors.New("legacy Jev key is too long")
	}
	return key, nil
}

func ownerOnly(path string, mode os.FileMode) error {
	if runtime.GOOS != "windows" && mode.Perm()&0o077 != 0 {
		// Do not include the filename in this message: callers can surface it
		// to an agent without revealing where a user's home is mounted.
		return errors.New("Jev settings permissions must be owner-only")
	}
	return nil
}
