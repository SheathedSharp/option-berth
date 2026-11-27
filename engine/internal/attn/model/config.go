package model

// The local model configuration is intentionally owned by the optional
// assessment and its setup command. Keeping it outside config.yaml means
// generic `oberth config get` output can never echo an API key, while the
// agent-side runner can still find the setting without a runtime service or a shell
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

// Protocol names the wire shape a configured endpoint speaks. It is a property
// of the endpoint, not of a vendor: a gateway that speaks the decisions schema
// and a local runtime that speaks OpenAI chat are configured the same way and
// differ only here. The empty value means ProtocolDecisions, which is what
// every configuration written before this field existed speaks.
type Protocol string

const (
	ProtocolDecisions  Protocol = "decisions"
	ProtocolOpenAIChat Protocol = "openai_chat"
	ProtocolAnthropic  Protocol = "anthropic"
)

// Mode is how much autonomy the assessment may express. It never widens what a
// command is allowed to do: strict only reads, assist reports and recommends,
// reconcile may mark a note eligible for a local restart a person already
// authorized. The empty value means ModeAssist once the assessment is enabled.
type Mode string

const (
	// ModeStrict reports the deterministic note and makes no recommendation.
	ModeStrict Mode = "strict"
	// ModeAssist recommends the next fixed option; it never acts.
	ModeAssist Mode = "assist"
	// ModeReconcile additionally marks notes whose evidence matches the
	// bounded local-restart rule as eligible for reconciliation.
	ModeReconcile Mode = "reconcile"
)

// NormalizeProtocol returns the effective protocol: the stored value, lower
// case and trimmed, or ProtocolDecisions when nothing is stored.
func NormalizeProtocol(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return string(ProtocolDecisions)
	}
	return value
}

// KnownProtocol reports whether a stored protocol value is one this build can
// speak. Empty is known: it means the default.
func KnownProtocol(value string) bool {
	switch Protocol(NormalizeProtocol(value)) {
	case ProtocolDecisions, ProtocolOpenAIChat, ProtocolAnthropic:
		return true
	}
	return false
}

// NormalizeMode returns the effective mode: the stored value, lower case and
// trimmed, or ModeAssist when nothing is stored.
func NormalizeMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return string(ModeAssist)
	}
	return value
}

// KnownMode reports whether a stored mode value is one this build understands.
func KnownMode(value string) bool {
	switch Mode(NormalizeMode(value)) {
	case ModeStrict, ModeAssist, ModeReconcile:
		return true
	}
	return false
}

// loopbackKey is the placeholder secret sent to a loopback endpoint that was
// configured without one. Local runtimes such as Ollama ignore authorization;
// the value exists so an OpenAI-compatible server still receives a well-formed
// header, and so the empty string keeps meaning "no key, refuse" everywhere
// else.
const loopbackKey = "ollama"

// LoopbackKeyPlaceholder is the exported spelling of that placeholder. A caller
// that stores it must never present it as a real secret.
const LoopbackKeyPlaceholder = loopbackKey

// LoopbackEndpoint reports whether an endpoint is a plain-HTTP address on this
// machine — the one shape that may be configured without a key.
func LoopbackEndpoint(endpoint string) bool { return loopbackEndpoint(endpoint) }

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
	// Protocol is the wire shape the endpoint speaks. Empty means
	// ProtocolDecisions, which keeps every file written before this field
	// existed exactly as valid as it was.
	Protocol string `json:"protocol,omitempty"`
	// Mode is the autonomy the assessment may express. Empty means ModeAssist.
	Mode string `json:"mode,omitempty"`
	// IncludeErrorLine says whether one redacted first-error line may be part
	// of a triage payload. Nil means yes: the line is already redacted, and a
	// triage without the error is a guess. A person who wants no log text at
	// all sets it to false.
	IncludeErrorLine *bool `json:"include_error_line,omitempty"`
}

// AllowsErrorLine is the nil-means-true form of IncludeErrorLine.
func (c LocalConfig) AllowsErrorLine() bool {
	return c.IncludeErrorLine == nil || *c.IncludeErrorLine
}

// EffectiveProtocol is the protocol a client built from this file speaks.
func (c LocalConfig) EffectiveProtocol() string { return NormalizeProtocol(c.Protocol) }

// EffectiveMode is the autonomy a client built from this file may express.
func (c LocalConfig) EffectiveMode() string { return NormalizeMode(c.Mode) }

// RedactedSettings is the stored shape without the secret, for `attn config`
// with no flags and for diagnostics.
func (c LocalConfig) RedactedSettings() map[string]any {
	state := map[string]any{
		"enabled":            c.Enabled,
		"endpoint":           c.Endpoint,
		"model":              c.Model,
		"timeout_ms":         c.Timeout().Milliseconds(),
		"protocol":           c.EffectiveProtocol(),
		"mode":               c.EffectiveMode(),
		"include_error_line": c.AllowsErrorLine(),
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
	// Protocol and mode are stored trimmed and lower case so two files that
	// mean the same thing hash and compare the same. Empty stays empty: the
	// default is applied at resolution, where it can be named as a default.
	c.Protocol = strings.ToLower(strings.TrimSpace(c.Protocol))
	if c.Protocol != "" && !KnownProtocol(c.Protocol) {
		return fmt.Errorf("attention settings protocol is unknown; use decisions, openai_chat or anthropic, or leave it empty")
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode != "" && !KnownMode(c.Mode) {
		return errors.New("attention settings mode is unknown; use strict, assist or reconcile, or leave it empty")
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
