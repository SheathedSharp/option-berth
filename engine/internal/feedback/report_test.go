package feedback

import (
	"strings"
	"testing"
	"time"
)

func validInput() Input {
	return Input{
		Kind:     "slow",
		Command:  "up",
		Version:  "v2.0.0",
		Commit:   "0123456789abcdef",
		Platform: "darwin/arm64",
		Services: -1,
		Now:      time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC),
		Redactor: Redactor{Home: "/Users/alice", User: "alice"},
	}
}

func TestBuildCarriesOnlyTheStatedVocabulary(t *testing.T) {
	in := validInput()
	in.ErrorCode = "observation_blind"
	in.Observation = "blind"
	in.Elapsed = "45s"
	in.Services = 30

	report, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if report.Kind != KindSlow || report.Command != CommandUp {
		t.Fatalf("report = %+v", report)
	}
	if report.Elapsed != "30s-2m" {
		t.Fatalf("elapsed bucket = %q, want 30s-2m", report.Elapsed)
	}
	if report.Services != "21+" {
		t.Fatalf("services bucket = %q, want 21+", report.Services)
	}
	if report.Commit != "0123456" {
		t.Fatalf("commit = %q, want the short form", report.Commit)
	}

	title := report.Title()
	if title != "[feedback] slow · up · v2.0.0 · darwin/arm64" {
		t.Fatalf("title = %q", title)
	}
	body := report.Body()
	for _, want := range []string{
		"<!-- " + ReportSchema + " -->",
		"**Kind:** slow",
		"**Command:** `oberth up`",
		"**Version:** v2.0.0 (commit 0123456)",
		"**Platform:** darwin/arm64",
		"**Elapsed:** 30s-2m",
		"**Services:** 21+",
		"**Error code:** observation_blind",
		"**Observation:** blind",
		"**Reported:** 2026-11-06",
		"no paths, logs, commands,",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "alice") || strings.Contains(body, "/Users") {
		t.Fatalf("body carries an identity:\n%s", body)
	}
}

func TestBuildRefusesWhatTheVocabularyDoesNotName(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Input)
	}{
		{"unknown kind", func(in *Input) { in.Kind = "annoying" }},
		{"unknown command", func(in *Input) { in.Command = "deploy" }},
		{"unknown error code", func(in *Input) { in.ErrorCode = "timeout" }},
		{"unknown observation", func(in *Input) { in.Observation = "empty" }},
		{"elapsed without a unit", func(in *Input) { in.Elapsed = "45" }},
		{"negative elapsed", func(in *Input) { in.Elapsed = "-5s" }},
		{"missing version", func(in *Input) { in.Version = "" }},
		{"missing platform", func(in *Input) { in.Platform = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mutate(&in)
			if _, err := Build(in); err == nil {
				t.Fatalf("Build accepted %+v", in)
			}
		})
	}
}

func TestBuildBucketsElapsed(t *testing.T) {
	for _, tc := range []struct{ given, want string }{
		{"100ms", "<1s"},
		{"2s", "1-5s"},
		{"20s", "5-30s"},
		{"45s", "30s-2m"},
		{"3m", "2m+"},
	} {
		in := validInput()
		in.Elapsed = tc.given
		report, err := Build(in)
		if err != nil {
			t.Fatalf("Build(%s): %v", tc.given, err)
		}
		if report.Elapsed != tc.want {
			t.Errorf("elapsed %s = %q, want %q", tc.given, report.Elapsed, tc.want)
		}
	}
}

func TestBuildBucketsServices(t *testing.T) {
	for _, tc := range []struct {
		given int
		want  string
	}{
		{-1, ""},
		{0, "none"},
		{1, "1"},
		{3, "2-5"},
		{12, "6-20"},
		{40, "21+"},
	} {
		in := validInput()
		in.Services = tc.given
		report, err := Build(in)
		if err != nil {
			t.Fatalf("Build(%d): %v", tc.given, err)
		}
		if report.Services != tc.want {
			t.Errorf("services %d = %q, want %q", tc.given, report.Services, tc.want)
		}
	}
}

func TestBuildRedactsTheNote(t *testing.T) {
	in := validInput()
	in.Note = "up stalled for /Users/alice/code/proj and alice@example.com was no help"
	report, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(report.Note, "/Users") || strings.Contains(report.Note, "alice") {
		t.Fatalf("note kept an identity: %q", report.Note)
	}
	if !strings.Contains(report.Body(), "**Note (redacted before sending):**") {
		t.Fatalf("body is missing the note section:\n%s", report.Body())
	}
}

// A note is the reporter's own words, so it must not be able to mention
// someone or reshape the issue with Markdown. Indenting it keeps both inert.
func TestBodyKeepsTheNoteVerbatimInsideACodeBlock(t *testing.T) {
	in := validInput()
	in.Note = "@maintainer please look\n```\nnot a fence to close"
	report, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	body := report.Body()
	if !strings.Contains(body, "\n    @maintainer please look\n") {
		t.Fatalf("the note is not an indented block:\n%s", body)
	}
	if !strings.Contains(body, "\n    ```\n") {
		t.Fatalf("a fence inside the note was not indented:\n%s", body)
	}
}

func TestFingerprintIgnoresTheNoteAndElapsed(t *testing.T) {
	first := validInput()
	first.Elapsed = "20s"
	first.Note = "one"
	second := validInput()
	second.Elapsed = "3m"
	second.Note = "another sighting"

	a, err := Build(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(second)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("fingerprints differ: %q vs %q", a.Fingerprint(), b.Fingerprint())
	}

	other := validInput()
	other.Kind = "stalled"
	c, err := Build(other)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint() == c.Fingerprint() {
		t.Fatal("a different kind must fingerprint differently")
	}
}

// The picker draws KindOptions. A kind without words would be a row a person
// cannot choose from, and the short line has to stay short enough to survive a
// one-line terminal row.
func TestKindOptionsCoverTheVocabulary(t *testing.T) {
	options := KindOptions()
	names := KindNames()
	if len(options) != len(names) {
		t.Fatalf("options = %d, want one per kind (%d)", len(options), len(names))
	}
	for i, option := range options {
		if string(option.Kind) != names[i] {
			t.Fatalf("option %d = %q, want %q", i, option.Kind, names[i])
		}
		if strings.TrimSpace(option.Label) == "" || strings.TrimSpace(option.Short) == "" {
			t.Errorf("%s has no words to show", option.Kind)
		}
		if len(option.Short) > 60 {
			t.Errorf("%s short line is %d characters, too wide for a picker row", option.Kind, len(option.Short))
		}
	}
}
