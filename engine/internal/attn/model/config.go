package model

// The local model configuration is intentionally owned by the optional
// assessment and its setup command. Keeping it outside config.yaml means
// generic `oberth config get` output can never echo an API key, while the
// agent-side runner can still find the setting without a daemon or a shell
// profile.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
)

const (
	LocalConfigSchema = "oberth.attn-settings/v1"
	maxLocalConfig    = 64 * 1024
	maxLocalKey       = 4096
	minLocalTimeout   = 250 * time.Millisecond
	maxLocalTimeout   = 2 * time.Minute
)

// LocalConfig is the on-disk shape written by `oberth attn config`.
// APIKey is never included in telemetry, attention artifacts, or diagnostic
// output. The file itself is required to be owner-only on Unix systems.
//
// Preset is a setup label only: it records which defaults filled Endpoint and
// Model. Runtime resolution never consults it.
type LocalConfig struct {
	Schema    string `json:"schema"`
	Enabled   bool   `json:"enabled"`
	Preset    string `json:"preset,omitempty"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	APIKey    string `json:"api_key"`
	TimeoutMS int    `json:"timeout_ms"`
}

// RedactedSettings is the stored shape without the secret, for `attn config`
// with no flags and for diagnostics.
func (c LocalConfig) RedactedSettings() map[string]any {
	state := map[string]any{
		"enabled":    c.Enabled,
		"endpoint":   c.Endpoint,
		"model":      c.Model,
		"timeout_ms": c.Timeout().Milliseconds(),
	}
	if c.Preset != "" {
		state["preset"] = c.Preset
	}
	if c.APIKey != "" {
		state["key_present"] = true
	}
	return state
}

// LocalConfigPath returns the path shared by setup commands and isolated runs.
func LocalConfigPath() string { return paths.AttnConfigPath() }

// LoadLocalConfig reads the local settings file. present distinguishes a
// missing file from an explicitly disabled file: once a person turns the
// assessment off, an environment key must not silently re-enable it.
func LoadLocalConfig() (cfg LocalConfig, present bool, err error) {
	return LoadLocalConfigFrom(paths.AttnConfigPath())
}

// LoadLocalConfigFrom is injectable for tests and isolated acceptance runs.
func LoadLocalConfigFrom(path string) (cfg LocalConfig, present bool, err error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return LocalConfig{}, false, nil
		}
		return LocalConfig{}, true, fmt.Errorf("reading attention settings: %w", statErr)
	}
	if !info.Mode().IsRegular() {
		return LocalConfig{}, true, errors.New("attention settings are not a regular file")
	}
	if info.Size() > maxLocalConfig {
		return LocalConfig{}, true, errors.New("attention settings file is too large")
	}
	if err := ownerOnly(path, info.Mode().Perm()); err != nil {
		return LocalConfig{}, true, err
	}
	file, err := os.Open(path)
	if err != nil {
		return LocalConfig{}, true, fmt.Errorf("reading attention settings: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxLocalConfig))
	if err := decoder.Decode(&cfg); err != nil {
		return LocalConfig{}, true, errors.New("attention settings are not valid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return LocalConfig{}, true, errors.New("attention settings contain more than one JSON value")
	}
	if err := cfg.Validate(); err != nil {
		return LocalConfig{}, true, err
	}
	return cfg, true, nil
}

// Validate checks the small, stable settings surface. It intentionally does
// not validate the remote model's existence; a network/provider error belongs
// to the adapter's ordinary unavailable path.
func (c *LocalConfig) Validate() error {
	if c.Schema != "" && c.Schema != LocalConfigSchema {
		return errors.New("attention settings schema is unsupported")
	}
	c.Preset = strings.ToLower(strings.TrimSpace(c.Preset))
	if c.Preset != "" {
		if _, _, ok := PresetDefaults(c.Preset); !ok {
			return fmt.Errorf("attention settings preset is unknown; use one of %s, or leave it empty", PresetNames())
		}
	}
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	if c.Endpoint != "" {
		if err := validateEndpoint(c.Endpoint); err != nil {
			return fmt.Errorf("attention settings: %w", err)
		}
	}
	c.Model = strings.TrimSpace(c.Model)
	if c.Enabled {
		if c.Endpoint == "" {
			return errors.New("attention settings endpoint is required when the assessment is enabled")
		}
		if c.Model == "" {
			return errors.New("attention settings model is required when the assessment is enabled")
		}
	}
	c.APIKey = strings.TrimSpace(c.APIKey)
	if len(c.APIKey) > maxLocalKey {
		return errors.New("attention settings API key is too long")
	}
	if c.TimeoutMS != 0 {
		timeout := time.Duration(c.TimeoutMS) * time.Millisecond
		if timeout < minLocalTimeout || timeout > maxLocalTimeout {
			return errors.New("attention settings timeout is outside the supported range")
		}
	}
	return nil
}

// validateEndpoint accepts the two shapes the adapter can actually talk to:
// HTTPS anywhere, or plain HTTP on a loopback host for a local deployment.
func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return errors.New("endpoint must be an absolute URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return errors.New("endpoint must be HTTPS, or HTTP on a loopback host")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Timeout returns the configured timeout or the adapter default.
func (c LocalConfig) Timeout() time.Duration {
	if c.TimeoutMS == 0 {
		return DefaultTimeout
	}
	timeout := time.Duration(c.TimeoutMS) * time.Millisecond
	if timeout < minLocalTimeout || timeout > maxLocalTimeout {
		return DefaultTimeout
	}
	return timeout
}

func ownerOnly(path string, mode os.FileMode) error {
	if runtime.GOOS != "windows" && mode.Perm()&0o077 != 0 {
		// Do not include the filename in this message: callers can surface it
		// to an agent without revealing where a user's home is mounted.
		return errors.New("attention settings permissions must be owner-only")
	}
	return nil
}
