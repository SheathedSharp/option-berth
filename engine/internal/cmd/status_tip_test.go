package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The optional-model hint is a courtesy, so every rule around it is about not
// becoming noise: it appears only in the human view, only when there is an
// anomaly, only while the assessment is off, and at most once per interval.

func TestTipDueHonoursTheRecordedTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jev-tip.json")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	// No record: the first read is the moment to say it once.
	if !tipDue(path, now) {
		t.Fatal("a missing record was not due")
	}

	recordAttnTip(path, now)

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
		"bad timestamp": `{"attn_shown_at":"yesterday"}`,
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

func TestRecordAttnTipIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "jev-tip.json")
	recordAttnTip(path, time.Now().UTC())
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
	// The anomaly is the classifier's verdict. It is deliberately not paired
	// with an AttnPath: that field only exists once the person opted in,
	// which is the state the tip is not shown in.
	withAnomaly := &statusDocument{attnEvent: true}

	t.Run("no anomaly", func(t *testing.T) {
		t.Setenv(noTipsEnv, "")
		if attnTipWanted(&statusDocument{}, now) {
			t.Fatal("the tip appeared with nothing to assess")
		}
	})

	t.Run("opted out", func(t *testing.T) {
		t.Setenv(noTipsEnv, "1")
		if attnTipWanted(withAnomaly, now) {
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
		if attnTipWanted(withAnomaly, now) {
			t.Fatal("the tip appeared on a non-terminal stdout")
		}
	})
}

// The tip must be reachable in the state it exists for. Requiring a published
// artifact made it dead code: consent gates the artifact, so with model off there
// is never a path to read, and the offer was silent in precisely the case it
// describes. The terminal is passed in so a passing pipe cannot stand in for a
// working decision.
func TestTipFiresWithAttnOffAndAnAnomaly(t *testing.T) {
	t.Setenv(noTipsEnv, "")
	t.Setenv("BERTH_HOME", t.TempDir())
	if attnEnabled() {
		t.Fatal("this case needs model to be off")
	}
	doc := &statusDocument{attnEvent: true}
	if !attnTipEligible(doc, time.Now().UTC(), true) {
		t.Fatal("a model-off read with a derived anomaly withheld the tip")
	}
}

// The published artifact no longer gates on consent: a read that derives an
// anomaly while the assessment is off still publishes the fact-only handoff and
// marks it as unavailable. The tip reads the verdict, not the consent.
func TestAnomalyVerdictSurvivesWithoutConsent(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	if attnEnabled() {
		t.Fatal("this case needs the assessment to be off")
	}
	exited := time.Now().UTC()
	doc := &statusDocument{Scope: display.Scope{Root: t.TempDir()}}
	doc.Worktree.Services = []state.Service{{
		Name:     "api",
		Running:  false,
		LastExit: &state.ServiceExit{Code: 1, Reason: "crashed", RunID: "r1", At: exited.Format(time.RFC3339Nano)},
	}}
	doc.Exits = []rpc.RunRecord{{
		Name: "api", Reason: "crashed", ID: "r1",
		ExitedAt: exited.Format(time.RFC3339Nano),
	}}
	publishAttention(doc)
	if doc.AttnPath == "" {
		t.Fatal("the fact-only handoff was not published without consent")
	}
	if doc.Attn == nil || doc.Attn.Enabled || doc.Attn.Assessment != "unavailable" {
		t.Fatalf("attn state without consent = %+v", doc.Attn)
	}
	if !doc.attnEvent {
		t.Fatal("the derived anomaly was discarded with the publication")
	}
}

// The hint is one dim line: it must name the feature, say it is optional, and
// give the setup and consent commands that turn it on.
func TestTipLineIsActionableAndShort(t *testing.T) {
	line := attnTipLine()
	for _, want := range []string{"model", "optional", "oberth attn config", "oberth attn on"} {
		if !strings.Contains(line, want) {
			t.Fatalf("tip %q does not mention %q", line, want)
		}
	}
}
