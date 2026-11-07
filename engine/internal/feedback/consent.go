// Package feedback implements the opt-in improvement report: what an
// installation agreed to send, the fixed vocabulary a report may carry, the
// redaction every note passes through, and the two ways a report reaches the
// project's issue tracker.
//
// It is about the engine itself. Nothing here reads a manifest, a log, a run,
// a worktree or the environment, because that is the only subject a person can
// consent to on the engine's behalf. The boundary is in docs/product.md.
package feedback

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ConsentSchema is the shape of the local consent file. A build that reads an
// unknown schema treats the file as unusable rather than as permission.
const ConsentSchema = "oberth.feedback.consent/v1"

// Decision is what the person answered. The three states are deliberately not
// a bool: a skip has to be told apart from a decline, or the next version
// could never ask again.
type Decision string

const (
	// DecisionEnabled: reports may be sent.
	DecisionEnabled Decision = "enabled"
	// DecisionDeclined: do not ask and do not send.
	DecisionDeclined Decision = "declined"
	// DecisionSkipped: not now. The version that asked is recorded, so an
	// upgrade may ask once more and nothing else does.
	DecisionSkipped Decision = "skipped"
)

// Consent is the local record of what this installation answered.
type Consent struct {
	Schema   string   `json:"schema"`
	Decision Decision `json:"decision"`
	// Version is the build that asked. A skip is remembered per version; a
	// decision is never asked again.
	Version   string `json:"version,omitempty"`
	DecidedAt string `json:"decided_at,omitempty"`
}

// Decide builds the record for an answer given at version.
func Decide(decision Decision, version string, now time.Time) Consent {
	return Consent{
		Schema:    ConsentSchema,
		Decision:  decision,
		Version:   version,
		DecidedAt: now.UTC().Format(time.RFC3339),
	}
}

// Enabled reports whether reports may be sent.
func (c Consent) Enabled() bool { return c.Decision == DecisionEnabled }

// LoadConsent reads the record at path. present is false only when the file
// does not exist. A file that cannot be parsed is an error: callers treat it
// as "no usable answer", never as consent.
func LoadConsent(path string) (consent Consent, present bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Consent{}, false, nil
		}
		return Consent{}, false, err
	}
	var decoded Consent
	if err := json.Unmarshal(data, &decoded); err != nil {
		return Consent{}, true, fmt.Errorf("reading %s: %w", path, err)
	}
	if decoded.Schema != ConsentSchema {
		return Consent{}, true, fmt.Errorf("%s: schema %q, want %q", path, decoded.Schema, ConsentSchema)
	}
	switch decoded.Decision {
	case DecisionEnabled, DecisionDeclined, DecisionSkipped:
	default:
		// Never hand back a value that could read as consent: an unusable
		// record is no answer at all.
		return Consent{}, true, fmt.Errorf("%s: decision %q is not enabled, declined or skipped",
			path, decoded.Decision)
	}
	return decoded, true, nil
}

// ShouldAsk reports whether the first screen may ask at currentVersion. It
// asks when nothing was ever written, and once more after an upgrade when the
// last answer was a skip. A decline is final until the person changes it with
// `oberth feedback on`.
func ShouldAsk(consent Consent, present bool, currentVersion string) bool {
	if !present {
		return true
	}
	if consent.Decision != DecisionSkipped {
		return false
	}
	return consent.Version != currentVersion
}

// WriteConsent writes c to path owner-only, atomically: a crash mid-write
// leaves the previous answer rather than half a file.
func WriteConsent(path string, c Consent) error {
	if c.Schema == "" {
		c.Schema = ConsentSchema
	}
	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the consent record: %w", err)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".feedback-*.json")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
