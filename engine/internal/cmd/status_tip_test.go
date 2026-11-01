package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The optional-Jev hint is a courtesy, so every rule around it is about not
// becoming noise: it appears only in the human view, only when there is an
// anomaly, only while Jev is off, and at most once per interval.

func TestTipDueHonoursTheRecordedTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jev-tip.json")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	// No record: the first read is the moment to say it once.
	if !tipDue(path, now) {
		t.Fatal("a missing record was not due")
	}

	recordJevTip(path, now)

	// Just after: not due.
	if tipDue(path, now.Add(time.Hour)) {
		t.Fatal("the tip repeated an hour later")
	}
	// Before the interval ends: still not due.
	if tipDue(path, now.Add(tipInterval-time.Minute)) {
		t.Fatal("the tip repeated before its interval")
	}
	// At the interval: due again.
	if !tipDue(path, now.Add(tipInterval)) {
		t.Fatal("the tip did not return after its interval")
	}
}

// The file is advisory. A corrupted or unreadable record must not silence the
// tip forever, because nothing else would ever surface it again.
func TestTipDueTreatsAGarbledRecordAsDue(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"not json":      "{",
		"bad timestamp": `{"jev_shown_at":"yesterday"}`,
		"empty object":  `{}`,
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if !tipDue(path, time.Now().UTC()) {
			t.Errorf("%s: tip was suppressed by an unreadable record", name)
		}
	}
}

func TestRecordJevTipIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "jev-tip.json")
	recordJevTip(path, time.Now().UTC())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("tip record mode = %o, want 600", info.Mode().Perm())
	}
}

// Three separate reasons a read must stay silent about the tip. Each is tested
// on its own so a future change cannot satisfy one by breaking another.
func TestTipIsSuppressed(t *testing.T) {
	t.Cleanup(func() { t.Setenv(noTipsEnv, "") })
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	withAnomaly := &statusDocument{AttentionPath: "/tmp/attention/abc.json"}

	t.Run("no anomaly", func(t *testing.T) {
		t.Setenv(noTipsEnv, "")
		if jevTipWanted(&statusDocument{}, now) {
			t.Fatal("the tip appeared with nothing to assess")
		}
	})

	t.Run("opted out", func(t *testing.T) {
		t.Setenv(noTipsEnv, "1")
		if jevTipWanted(withAnomaly, now) {
			t.Fatal("the tip ignored BERTH_NO_TIPS")
		}
	})

	t.Run("not a terminal", func(t *testing.T) {
		t.Setenv(noTipsEnv, "")
		// `go test` runs with stdout as a pipe, so this is the real
		// non-interactive case rather than a simulated one.
		if isTerminalStdout() {
			t.Skip("stdout is a terminal in this environment")
		}
		if jevTipWanted(withAnomaly, now) {
			t.Fatal("the tip appeared on a non-terminal stdout")
		}
	})
}

// The hint is one dim line: it must name the feature, say it is optional, and
// give the one command that turns it on.
func TestTipLineIsActionableAndShort(t *testing.T) {
	line := jevTipLine()
	for _, want := range []string{"Jev", "optional", "oberth jev enable"} {
		if !strings.Contains(line, want) {
			t.Fatalf("tip %q does not mention %q", line, want)
		}
	}
}
