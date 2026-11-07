package feedback

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestMissingConsentIsNotAnAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.json")
	consent, present, err := LoadConsent(path)
	if err != nil || present {
		t.Fatalf("LoadConsent on a missing file = (%+v, %v, %v), want no answer and no error",
			consent, present, err)
	}
	if !ShouldAsk(consent, present, "v2.0.0") {
		t.Fatal("a missing record must allow the question")
	}
}

func TestConsentRoundTripIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "feedback.json")
	record := Decide(DecisionEnabled, "v2.0.0", time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	if err := WriteConsent(path, record); err != nil {
		t.Fatalf("WriteConsent: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("consent mode = %o, want 600", mode)
		}
	}
	got, present, err := LoadConsent(path)
	if err != nil || !present {
		t.Fatalf("LoadConsent = (%+v, %v, %v)", got, present, err)
	}
	if got.Decision != DecisionEnabled || got.Version != "v2.0.0" || !got.Enabled() {
		t.Fatalf("round trip = %+v", got)
	}
	if got.DecidedAt != "2026-11-06T10:00:00Z" {
		t.Fatalf("decided_at = %q", got.DecidedAt)
	}
}

func TestShouldAskOnlyOncePerVersionAfterASkip(t *testing.T) {
	cases := []struct {
		name    string
		consent Consent
		present bool
		current string
		want    bool
	}{
		{"never asked", Consent{}, false, "v2.0.0", true},
		{"enabled is final", consentWith(DecisionEnabled, "v1.0.0"), true, "v2.0.0", false},
		{"declined is final", consentWith(DecisionDeclined, "v1.0.0"), true, "v2.0.0", false},
		{"skipped at this version", consentWith(DecisionSkipped, "v2.0.0"), true, "v2.0.0", false},
		{"skipped at an older version", consentWith(DecisionSkipped, "v1.0.0"), true, "v2.0.0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldAsk(tc.consent, tc.present, tc.current); got != tc.want {
				t.Fatalf("ShouldAsk = %v, want %v", got, tc.want)
			}
		})
	}
}

func consentWith(decision Decision, version string) Consent {
	return Consent{Schema: ConsentSchema, Decision: decision, Version: version}
}

func TestLoadConsentRejectsWhatItCannotTrust(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"unknown schema", `{"schema":"oberth.feedback.consent/v2","decision":"enabled"}`},
		{"unknown decision", `{"schema":"oberth.feedback.consent/v1","decision":"maybe"}`},
		{"not json", `enabled`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "feedback.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			consent, present, err := LoadConsent(path)
			if err == nil {
				t.Fatalf("LoadConsent accepted %s: %+v", tc.body, consent)
			}
			if !present {
				t.Fatal("a file that exists must report present, even when it is unusable")
			}
			if consent.Enabled() {
				t.Fatal("an unusable record must never read as consent")
			}
		})
	}
}
