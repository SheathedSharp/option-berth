package jev

// `oberth jev enable` is the one step that turns the optional assessment on.
// It stores the key the person chose, proves the key works with a single
// redacted probe, and only then writes the owner-only settings file.
//
// Two rules shape this file:
//   - a probe that did not succeed writes nothing, so a typo cannot leave a
//     half-configured provider behind that fails on every later task;
//   - the key is never echoed, never put in telemetry, never returned from a
//     function that a command could print by accident.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attention"
)

// KeySourceKind says where `enable` should read the secret from. The three
// forms exist so a script, a CI job and a person at a terminal all have a path
// that does not put the key in an argument list.
type KeySourceKind string

const (
	// KeyFromStdin reads the key from the first line of standard input, so it
	// never appears in a process listing.
	KeyFromStdin KeySourceKind = "stdin"
	// KeyFromEnv reads it from a named environment variable.
	KeyFromEnv KeySourceKind = "env"
	// KeyFromPrompt reads it from the terminal with echo disabled.
	KeyFromPrompt KeySourceKind = "prompt"
)

// EnableOptions is one `oberth jev enable` request.
type EnableOptions struct {
	Provider string
	Endpoint string
	Model    string
	Timeout  time.Duration
	KeyKind  KeySourceKind
	// KeyEnv names the variable for KeyFromEnv.
	KeyEnv string
	// KeyValue is the secret for the stdin and prompt paths. It is never
	// logged; the caller clears it after use.
	KeyValue string
}

// SettingsFrom turns the request into the on-disk shape, without the key.
func (o EnableOptions) SettingsFrom(cfg LocalJevConfig) LocalJevConfig {
	next := cfg
	next.Schema = LocalJevConfigSchema
	next.Enabled = true
	if strings.TrimSpace(o.Provider) != "" {
		next.Provider = strings.ToLower(strings.TrimSpace(o.Provider))
	}
	if next.Provider == "" {
		next.Provider = ProviderOpenRouter
	}
	if strings.TrimSpace(o.Endpoint) != "" {
		next.Endpoint = strings.TrimSpace(o.Endpoint)
	}
	if strings.TrimSpace(o.Model) != "" {
		next.Model = strings.TrimSpace(o.Model)
	}
	if o.Timeout > 0 {
		next.TimeoutMS = int(o.Timeout / time.Millisecond)
	}
	return next
}

// ResolveKey reads the secret from the chosen source. An empty result is an
// error, not a silently disabled key: the whole point of `enable` is a working
// configuration.
func (o EnableOptions) ResolveKey(env func(string) string, stdinLine func() (string, error)) (string, error) {
	switch o.KeyKind {
	case KeyFromEnv:
		if strings.TrimSpace(o.KeyEnv) == "" {
			return "", errors.New("--key-env needs a variable name")
		}
		value := strings.TrimSpace(env(o.KeyEnv))
		if value == "" {
			return "", fmt.Errorf("environment variable %s is empty", o.KeyEnv)
		}
		return value, nil
	case KeyFromPrompt:
		value := strings.TrimSpace(o.KeyValue)
		if value == "" {
			return "", errors.New("no key was entered")
		}
		return value, nil
	default:
		line, err := stdinLine()
		if err != nil {
			return "", err
		}
		if line = strings.TrimSpace(line); line == "" {
			return "", errors.New("standard input did not contain a key")
		}
		return line, nil
	}
}

// ProbeResult is what a single network probe may report. Reason is one of the
// fixed codes; Text is a bounded, redacted sentence a person can read.
type ProbeResult struct {
	OK     bool
	Reason string
	Text   string
	Model  string
	// Latency is how long the provider took to answer.
	Latency time.Duration
}

// ProbeOnce sends one synthetic, redacted event through the real client. It is
// the same request shape a real assessment uses, so a key that passes here is a
// key that works; nothing about the person's worktree is included.
func ProbeOnce(ctx context.Context, cfg LocalJevConfig) ProbeResult {
	return ProbeWithClient(ctx, cfg, nil)
}

// ProbeWithClient is ProbeOnce with an injectable HTTP client, so a test can
// point the probe at a local server without touching http.DefaultClient.
func ProbeWithClient(ctx context.Context, cfg LocalJevConfig, httpClient *http.Client) ProbeResult {
	started := time.Now()
	task := TaskContext{
		Task: "Connection check: confirm this key can answer a structured assessment.",
		// One read-only option is enough to exercise the question shape without
		// asking the provider to rank a choice it cannot make meaningfully.
		AuthorizedOptions: []string{"inspect_logs"},
	}
	artifact := syntheticProbeArtifact()
	client := Client{
		Endpoint:   firstNonEmpty(cfg.Endpoint, defaultEndpointFor(cfg.Provider)),
		Model:      firstNonEmpty(cfg.Model, defaultModelFor(cfg.Provider)),
		APIKey:     cfg.APIKey,
		Timeout:    cfg.Timeout(),
		HTTPClient: httpClient,
	}
	_, err := client.Evaluate(ctx, artifact, task)
	result := ProbeResult{Latency: time.Since(started), Model: client.Model}
	if err != nil {
		result.Reason = ReasonFor(err)
		result.Text = ReasonHint(result.Reason)
		return result
	}
	result.OK = true
	result.Text = "the provider answered a synthetic assessment"
	return result
}

func defaultEndpointFor(provider string) string {
	if strings.EqualFold(provider, ProviderOpenRouter) {
		return DefaultOpenRouterEndpoint
	}
	return DefaultJevEndpoint
}

func defaultModelFor(provider string) string {
	if strings.EqualFold(provider, ProviderOpenRouter) {
		return DefaultOpenRouterModel
	}
	return DefaultJevModel
}

// syntheticProbeArtifact is a fixed, dated event so the probe request is a
// valid assessment request without any local facts.
func syntheticProbeArtifact() attention.Artifact {
	at := time.Now().UTC()
	code := 1
	return attention.Artifact{
		Schema:        attention.Schema,
		EventID:       "probe-connection-check",
		StateRevision: "probe-connection-check",
		WorktreeRoot:  "/probe",
		GeneratedAt:   at.Format(time.RFC3339Nano),
		FreshUntil:    at.Add(5 * time.Minute).Format(time.RFC3339Nano),
		Event: attention.Event{
			Kind: attention.EventServiceFailed, Service: "probe", Reason: "start_failed",
			ExitCode: &code, At: at.Format(time.RFC3339Nano),
		},
		Evidence: []attention.Evidence{{Ref: "status.worktree.services[probe].last_exit"}},
		Options:  []attention.Option{{ID: "inspect_logs"}, {ID: "continue_without_action"}},
	}
}

// WriteSettings atomically stores an owner-only settings file. The directory is
// created owner-only too: a settings file whose parent is world-readable is not
// "0600 in spirit".
func WriteSettings(path string, cfg LocalJevConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return errors.New("refusing to write settings with an empty key")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("restrict settings directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".jev-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict temporary settings file: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close settings: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("publish settings: %w", err)
	}
	return nil
}

// DisableSettings turns the committed setting off without deleting the key, so
// a person who only wants Jev to stop does not have to paste the key again.
func DisableSettings(path string, cfg LocalJevConfig, forget bool) error {
	next := cfg
	next.Schema = LocalJevConfigSchema
	next.Enabled = false
	if forget {
		next.APIKey = ""
	}
	if next.APIKey == "" {
		// WriteSettings refuses an empty key because `enable` must not publish a
		// configuration that cannot work. Disabling is the one case where an
		// empty key is the intended state, so write it directly.
		return writeDisabledSettings(path, next)
	}
	return WriteSettings(path, next)
}

func writeDisabledSettings(path string, cfg LocalJevConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".jev-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// KeyFingerprint is the short, non-reversible label a status line may show so a
// person can tell two keys apart without either being printable.
func KeyFingerprint(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:8]
}
