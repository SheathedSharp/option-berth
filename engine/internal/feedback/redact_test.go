package feedback

import (
	"errors"
	"strings"
	"testing"
)

func TestRedactBlanksIdentifyingShapes(t *testing.T) {
	redactor := Redactor{Home: "/Users/alice", User: "alice"}
	cases := []struct{ name, in, want string }{
		{"home path", "up failed in /Users/alice/code/proj", "up failed in <path>"},
		{"tilde path", "cd ~/code/proj && oberth up", "cd <path> && oberth up"},
		{"other absolute path", "see /var/log/oberth/daemon.log", "see <path>"},
		{"windows path", `C:\Users\alice\proj failed`, `<path> failed`},
		{"email", "alice@example.com reported it", "<email> reported it"},
		{"url", "helper at https://internal.example.com/x kept timing out", "helper at <url> kept timing out"},
		{"ipv4", "could not bind 192.168.1.10", "could not bind <ip>"},
		{"login name without a path", "alice saw no output", "<user> saw no output"},
		{
			"clean text is untouched",
			"oberth up --wait took 3m with observation_blind on v2.0.0",
			"oberth up --wait took 3m with observation_blind on v2.0.0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := redactor.Redact(tc.in)
			if err != nil {
				t.Fatalf("Redact(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactRefusesSecretsInsteadOfGuessing(t *testing.T) {
	redactor := Redactor{Home: "/Users/alice", User: "alice"}
	for _, note := range []string{
		"token ghp_" + strings.Repeat("a", 30),
		"github_pat_" + strings.Repeat("b", 30),
		"sk-" + strings.Repeat("c", 24),
		"AKIA" + strings.Repeat("D", 16),
		"Authorization: Bearer " + strings.Repeat("e", 24),
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
	} {
		if _, err := redactor.Redact(note); !errors.Is(err, ErrSecret) {
			t.Errorf("Redact(%q) error = %v, want ErrSecret", note, err)
		}
	}
}

func TestNormalizeNoteBoundsAndTrims(t *testing.T) {
	redactor := Redactor{}
	if got, err := redactor.NormalizeNote("   "); err != nil || got != "" {
		t.Fatalf("blank note = (%q, %v), want empty", got, err)
	}
	if _, err := redactor.NormalizeNote(strings.Repeat("x", MaxNoteBytes+1)); err == nil {
		t.Fatal("an over-long note must be refused, not cut")
	}
	if _, err := redactor.Redact("\xff\xfe"); err == nil {
		t.Fatal("invalid UTF-8 must be refused")
	}
}
