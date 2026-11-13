package feedback

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// stubGH replaces the one gh seam for a test. Tests that use it must not run in
// parallel: the seam is package state, exactly like the binary it stands in for.
func stubGH(t *testing.T, fn func(context.Context, ...string) ([]byte, error)) {
	t.Helper()
	previous := ghRun
	ghRun = fn
	t.Cleanup(func() { ghRun = previous })
}

func TestGHLoginReadsTheAccountFromTheStructuredStatus(t *testing.T) {
	stubGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "auth status --json hosts" {
			t.Fatalf("gh args = %v", args)
		}
		return []byte(`{"hosts":{"github.com":[{"state":"success","active":true,"login":"zayJu"}]}}`), nil
	})
	account := GHLogin(context.Background())
	if !account.Available || account.Account != "zayJu" || account.Reason != "" {
		t.Fatalf("GHLogin = %+v", account)
	}
}

// An older gh has no --json; a successful status still proves a credential is
// present, and the account is simply left unnamed rather than guessed.
func TestGHLoginFallsBackToTheExitStatus(t *testing.T) {
	stubGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "--json") {
			return nil, errors.New("unknown flag: --json")
		}
		return nil, nil
	})
	account := GHLogin(context.Background())
	if !account.Available || account.Account != "" || account.Reason != "" {
		t.Fatalf("GHLogin = %+v", account)
	}
}

func TestGHLoginReportsAMissingOrSignedOutGH(t *testing.T) {
	stubGH(t, func(context.Context, ...string) ([]byte, error) {
		return nil, &exec.Error{Name: "gh", Err: exec.ErrNotFound}
	})
	if account := GHLogin(context.Background()); account.Available || account.Reason != "gh is not installed" {
		t.Fatalf("missing gh = %+v", account)
	}

	stubGH(t, func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	})
	if account := GHLogin(context.Background()); account.Available || account.Reason != "gh is not signed in" {
		t.Fatalf("signed-out gh = %+v", account)
	}
}

// A credential for another host cannot file into github.com, and saying
// "available" would send the report down a path that then fails.
func TestGHLoginRefusesACredentialForAnotherHost(t *testing.T) {
	stubGH(t, func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"hosts":{"gitlab.com":[{"state":"success","active":true,"login":"zayJu"}]}}`), nil
	})
	account := GHLogin(context.Background())
	if account.Available || account.Reason != "gh is not signed in to github.com" {
		t.Fatalf("GHLogin = %+v", account)
	}
}

func TestIssueNumberOnlyAcceptsFiledIssueURLs(t *testing.T) {
	cases := []struct {
		url  string
		want int
		ok   bool
	}{
		{"https://github.com/SheathedSharp/option-berth/issues/42", 42, true},
		{"https://github.com/SheathedSharp/option-berth/issues/42/", 42, true},
		{" https://github.com/SheathedSharp/option-berth/issues/7 ", 7, true},
		{"https://github.com/SheathedSharp/option-berth/issues/new?title=x", 0, false},
		{"https://example.com/SheathedSharp/option-berth/issues/42", 0, false},
		{"https://github.com/SheathedSharp/option-berth/pull/42", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := IssueNumber(tc.url)
		if got != tc.want || ok != tc.ok {
			t.Errorf("IssueNumber(%q) = (%d, %v), want (%d, %v)", tc.url, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLookupIssuesAsksOneFilteredListing(t *testing.T) {
	var seen []string
	stubGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		seen = args
		return []byte(`[{"number":42,"state":"CLOSED","stateReason":"COMPLETED",
			"title":"[feedback] slow · up · v2.2.0 · darwin/arm64",
			"url":"https://github.com/SheathedSharp/option-berth/issues/42",
			"updatedAt":"2026-11-13T01:00:00Z","closedAt":"2026-11-13T01:00:00Z",
			"labels":[{"name":"fixed"}]}]`), nil
	})
	lookup := LookupIssues(context.Background(), GHAccount{Available: true, Account: "zayJu"}, DefaultRepo, []int{42})
	if lookup.Reason != "" {
		t.Fatalf("lookup reason = %q", lookup.Reason)
	}
	issue, ok := lookup.Issues[42]
	if !ok {
		t.Fatalf("issues = %+v", lookup.Issues)
	}
	if issue.State != "closed" || issue.StateReason != "completed" || len(issue.Labels) != 1 || issue.Labels[0] != "fixed" {
		t.Fatalf("issue = %+v", issue)
	}
	joined := strings.Join(seen, " ")
	for _, want := range []string{"issue list", "--state all", "--search", "[feedback] in:title", "--json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gh args are missing %q: %s", want, joined)
		}
	}
}

func TestOutcomeForClassifiesEveryState(t *testing.T) {
	open := Issue{Number: 1, State: "open", UpdatedAt: "2026-11-13T00:00:00Z"}
	closed := Issue{Number: 2, State: "closed", StateReason: "completed", ClosedAt: "2026-11-12T00:00:00Z"}
	notPlanned := Issue{Number: 3, State: "closed", StateReason: "not_planned"}
	lookup := IssueLookup{Issues: map[int]Issue{1: open, 2: closed, 3: notPlanned, 4: open}}

	cases := []struct {
		name  string
		entry Entry
		want  string
	}{
		{"open", Entry{Channel: "gh", URL: "https://github.com/o/r/issues/1"}, OutcomeOpen},
		{"resolved", Entry{Channel: "gh", URL: "https://github.com/o/r/issues/2"}, OutcomeResolved},
		{"not planned", Entry{Channel: "gh", URL: "https://github.com/o/r/issues/3"}, OutcomeNotPlanned},
		{"no number", Entry{Channel: "gh", URL: "not-a-url"}, OutcomeUnknown},
		{"missing listing", Entry{Channel: "gh", URL: "https://github.com/o/r/issues/99"}, OutcomeUnknown},
		{"prefilled", Entry{Channel: "url", URL: "https://github.com/o/r/issues/new"}, OutcomeNotFiled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OutcomeFor(tc.entry, lookup); got.Status != tc.want {
				t.Fatalf("OutcomeFor = %+v, want status %q", got, tc.want)
			}
		})
	}
	if got := OutcomeFor(Entry{Channel: "gh", URL: "https://github.com/o/r/issues/1"}, IssueLookup{Reason: "gh is not signed in"}); got.Status != OutcomeUnknown || got.Reason == "" {
		t.Fatalf("unavailable lookup = %+v", got)
	}
}

func TestTotalsAndSummaryKeepFiledAndPrefilledApart(t *testing.T) {
	entries := []Entry{
		{At: "2026-11-11T00:00:00Z", Channel: "gh", URL: "https://github.com/o/r/issues/1"},
		{At: "2026-11-12T00:00:00Z", Channel: "gh", URL: "https://github.com/o/r/issues/2"},
		{At: "2026-11-13T00:00:00Z", Channel: "url", URL: "https://github.com/o/r/issues/new"},
	}
	totals := Totals(entries)
	if totals.Total != 3 || totals.Filed != 2 || totals.Prefilled != 1 || totals.LastAt != "2026-11-13T00:00:00Z" {
		t.Fatalf("Totals = %+v", totals)
	}
	outcomes := []Outcome{{Status: OutcomeOpen}, {Status: OutcomeResolved}, {Status: OutcomeNotFiled}}
	summary := Summarize(entries, outcomes)
	if summary.Total != 3 || summary.Filed != 2 || summary.Prefilled != 1 || summary.Open != 1 || summary.Resolved != 1 || summary.Unknown != 0 {
		t.Fatalf("Summarize = %+v", summary)
	}
	if numbers := FiledIssueNumbers(entries); len(numbers) != 2 || numbers[0] != 1 || numbers[1] != 2 {
		t.Fatalf("FiledIssueNumbers = %v", numbers)
	}
}
