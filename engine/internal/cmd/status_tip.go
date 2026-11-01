package cmd

// One line, at most once every few days, telling a person that the optional
// assessment exists. It is a suggestion, not a warning: nothing is wrong with
// status, and nothing about the facts changes when Jev is off.
//
// The rules that keep it from becoming noise:
//   - only on a terminal, and only in the human view (`--json` and `--brief`
//     never carry it, because a script and an agent are not reading a tip);
//   - only when Jev is off and the event it would assess is actually present;
//   - at most once per tipInterval, recorded in BERTH_HOME;
//   - BERTH_NO_TIPS=1 turns it off entirely.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/jev"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
)

const (
	// tipInterval is deliberately long: this is a feature a person either wants
	// or does not, and repeating the offer weekly would read as nagging.
	tipInterval = 14 * 24 * time.Hour
	// noTipsEnv silences every tip without touching the files.
	noTipsEnv = "BERTH_NO_TIPS"
)

// tipRecord is the whole file: when the tip was last shown. Keeping the shape
// minimal means a future tip can add a field without a migration.
type tipRecord struct {
	ShownAt string `json:"jev_shown_at,omitempty"`
}

// jevTipWanted reports whether this status read should end with the hint.
func jevTipWanted(doc *statusDocument, now time.Time) bool {
	if doc == nil || strings.TrimSpace(doc.AttentionPath) == "" {
		// No anomaly means nothing for the assessment to look at; a tip here
		// would describe a feature with nothing to do.
		return false
	}
	if strings.TrimSpace(os.Getenv(noTipsEnv)) == "1" {
		return false
	}
	if !isTerminalStdout() {
		return false
	}
	return tipDue(paths.JevTipPath(), now)
}

// tipDue reads the record and answers whether enough time has passed. A missing
// or unreadable record counts as due: the file is advisory, and failing closed
// would mean a corrupted file permanently silences the tip.
func tipDue(path string, now time.Time) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var record tipRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return true
	}
	shown, err := time.Parse(time.RFC3339Nano, record.ShownAt)
	if err != nil {
		return true
	}
	return now.Sub(shown) >= tipInterval
}

// recordJevTip moves the timestamp forward. A write failure is silent: the tip
// is a courtesy, and a read-only BERTH_HOME must not turn it into an error.
func recordJevTip(path string, now time.Time) {
	record := tipRecord{ShownAt: now.UTC().Format(time.RFC3339Nano)}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".jev-tip-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(name, path)
}

// jevTipLine is the whole hint: what it is, that it is optional, and how to
// turn it on. One line, because it sits under a status table a reader is
// scanning.
func jevTipLine() string {
	return display.Dim("Jev: an optional model can assess this anomaly; `oberth jev enable` turns it on")
}

// isTerminalStdout follows the same character-device test the rest of the CLI
// uses for color and prompts: a pipe is not a person.
func isTerminalStdout() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// jevEnabled reports the committed consent only: a one-shot BERTH_JEV in the
// current shell is a decision for that process, not a reason to stop telling
// the next person the feature exists.
func jevEnabled() bool {
	cfg, present, err := jev.LoadLocalJevConfig()
	if err != nil {
		return false
	}
	return jev.Resolve(jev.ResolveInput{Settings: cfg, SettingsPresent: present}).Enabled
}
