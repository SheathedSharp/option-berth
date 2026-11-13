package feedback

// The other half of a filed report: who it will be filed as, and what happened
// to it afterwards. The local ledger is the list of reports this installation
// produced; GitHub is the only place that knows whether an issue was resolved,
// so these reads go through the person's own `gh` login and never through a
// token the product stores.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	ghAuthTimeout = 3 * time.Second
	ghListTimeout = 15 * time.Second
	// ghIssueLimit bounds the listing behind `feedback log`. Reports are filed
	// with a `[feedback]` title, so this filter returns this product's issues
	// rather than every issue in the repository.
	ghIssueLimit = 100
	ghIssueTitle = "[feedback]"
)

// ghRun is the one place a gh subcommand is executed. Tests replace it to
// describe a machine without gh or with a scripted answer; production runs the
// real binary and returns its stdout.
var ghRun = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	return cmd.Output()
}

// GHAccount is how this installation reaches GitHub: the account `gh` is
// signed in as, or the reason it cannot file through it.
type GHAccount struct {
	Available bool   `json:"available"`
	Account   string `json:"account,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// GHLogin reads the local gh credential. It never sends the report, creates
// nothing and does not use a token any other component supplied.
func GHLogin(ctx context.Context) GHAccount {
	out, err := ghAuth(ctx, "auth", "status", "--json", "hosts")
	if err == nil {
		if account, known := githubAccount(out); known {
			if account == "" {
				return GHAccount{Reason: "gh is not signed in to github.com"}
			}
			return GHAccount{Available: true, Account: account}
		}
	}
	// An older gh without --json still answers the one question that matters:
	// is a credential present? It cannot name the account, which is why the
	// structured form is tried first.
	if _, fallbackErr := ghAuth(ctx, "auth", "status"); fallbackErr == nil {
		return GHAccount{Available: true}
	}
	return GHAccount{Reason: ghReason(err)}
}

// ghAuth runs one read with a fresh short deadline. The fallback must not
// inherit whatever the first attempt left of a shared timeout.
func ghAuth(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ghAuthTimeout)
	defer cancel()
	return ghRun(ctx, args...)
}

func ghReason(err error) string {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "gh is not installed"
	case errors.Is(err, context.DeadlineExceeded):
		return "gh did not answer in time"
	default:
		return "gh is not signed in"
	}
}

type ghAuthHosts struct {
	Hosts map[string][]ghAuthHost `json:"hosts"`
}

type ghAuthHost struct {
	State  string `json:"state"`
	Active bool   `json:"active"`
	Login  string `json:"login"`
}

// githubAccount picks the github.com login from `gh auth status --json hosts`.
// known is false when the document is not the expected shape, which is how a
// caller tells "an old gh" from "signed in somewhere else".
func githubAccount(out []byte) (account string, known bool) {
	var decoded ghAuthHosts
	if err := json.Unmarshal(out, &decoded); err != nil {
		return "", false
	}
	hosts, ok := decoded.Hosts["github.com"]
	if !ok {
		return "", true
	}
	for _, host := range hosts {
		if host.Active && host.State == "success" && strings.TrimSpace(host.Login) != "" {
			return host.Login, true
		}
	}
	for _, host := range hosts {
		if host.State == "success" && strings.TrimSpace(host.Login) != "" {
			return host.Login, true
		}
	}
	return "", true
}

// Issue is the GitHub-side context of one filed report. It is read, never
// written: the product has no authority over the repository's tracker.
type Issue struct {
	Number      int      `json:"number"`
	State       string   `json:"state"`                  // open or closed
	StateReason string   `json:"state_reason,omitempty"` // completed or not_planned
	Title       string   `json:"title,omitempty"`
	URL         string   `json:"url,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	ClosedAt    string   `json:"closed_at,omitempty"`
}

// IssueLookup is the answer GitHub gave, or the reason it could not be asked.
// A missing issue is not an error: the repository listing is bounded.
type IssueLookup struct {
	Issues map[int]Issue
	Reason string
}

// LookupIssues reads the report issues in repo. numbers is the set this
// installation filed; the call itself is one filtered listing, because the
// reports all carry the same title prefix.
func LookupIssues(ctx context.Context, account GHAccount, repo string, numbers []int) IssueLookup {
	lookup := IssueLookup{Issues: make(map[int]Issue, len(numbers))}
	if len(numbers) == 0 {
		return lookup
	}
	if !account.Available {
		lookup.Reason = account.Reason
		return lookup
	}
	ctx, cancel := context.WithTimeout(ctx, ghListTimeout)
	defer cancel()
	out, err := ghRun(ctx, "issue", "list",
		"--repo", repo,
		"--state", "all",
		"--search", ghIssueTitle+" in:title",
		"--limit", strconv.Itoa(ghIssueLimit),
		"--json", "number,state,stateReason,title,url,labels,updatedAt,closedAt")
	if err != nil {
		lookup.Reason = "gh could not read the repository's issues"
		return lookup
	}
	var listed []ghIssue
	if err := json.Unmarshal(out, &listed); err != nil {
		lookup.Reason = "gh returned an unreadable issue listing"
		return lookup
	}
	for _, item := range listed {
		issue := item.issue()
		lookup.Issues[issue.Number] = issue
	}
	return lookup
}

type ghIssue struct {
	Number      int    `json:"number"`
	State       string `json:"state"`
	StateReason string `json:"stateReason"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	UpdatedAt   string `json:"updatedAt"`
	ClosedAt    string `json:"closedAt"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (g ghIssue) issue() Issue {
	issue := Issue{
		Number:      g.Number,
		State:       strings.ToLower(g.State),
		StateReason: strings.ToLower(g.StateReason),
		Title:       g.Title,
		URL:         g.URL,
		UpdatedAt:   g.UpdatedAt,
		ClosedAt:    g.ClosedAt,
	}
	for _, label := range g.Labels {
		if label.Name != "" {
			issue.Labels = append(issue.Labels, label.Name)
		}
	}
	return issue
}

// Outcome statuses. They are a closed vocabulary because an agent branches on
// them; a human reads the same word.
const (
	OutcomeOpen       = "open"
	OutcomeResolved   = "resolved"
	OutcomeNotPlanned = "not_planned"
	OutcomeUnknown    = "unknown"
	OutcomeNotFiled   = "not_filed"
)

// Outcome is what `feedback log` can say about one ledger entry.
type Outcome struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Issue  *Issue `json:"issue,omitempty"`
}

// OutcomeFor classifies one entry. A `url` channel entry is not a filed issue:
// the person still had to press submit, so it is never counted as filed.
func OutcomeFor(entry Entry, lookup IssueLookup) Outcome {
	if entry.Channel != "gh" {
		return Outcome{Status: OutcomeNotFiled}
	}
	number, ok := IssueNumber(entry.URL)
	if !ok {
		return Outcome{Status: OutcomeUnknown, Reason: "the local record has no issue number"}
	}
	if lookup.Reason != "" {
		return Outcome{Status: OutcomeUnknown, Reason: lookup.Reason}
	}
	issue, ok := lookup.Issues[number]
	if !ok {
		return Outcome{Status: OutcomeUnknown, Reason: "the issue was not in the repository listing"}
	}
	out := Outcome{Status: OutcomeOpen, Issue: &issue}
	if issue.State == "closed" {
		out.Status = OutcomeResolved
		if issue.StateReason == "not_planned" {
			out.Status = OutcomeNotPlanned
		}
	}
	return out
}

// IssueNumber reads the number out of a filed issue URL. Anything that is not a
// github.com issue URL is not treated as a filed report.
func IssueNumber(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	const prefix = "https://github.com/"
	if !strings.HasPrefix(raw, prefix) {
		return 0, false
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(raw, prefix), "/"), "/")
	if len(parts) != 4 || parts[2] != "issues" {
		return 0, false
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

// FiledIssueNumbers is the set a lookup needs: unique, filed reports only.
func FiledIssueNumbers(entries []Entry) []int {
	seen := make(map[int]bool, len(entries))
	numbers := make([]int, 0, len(entries))
	for _, entry := range entries {
		if entry.Channel != "gh" {
			continue
		}
		number, ok := IssueNumber(entry.URL)
		if !ok || seen[number] {
			continue
		}
		seen[number] = true
		numbers = append(numbers, number)
	}
	return numbers
}

// LedgerTotals is the local half of the picture: how many reports this
// installation produced, and how many of them actually reached GitHub.
type LedgerTotals struct {
	Total     int    `json:"total"`
	Filed     int    `json:"filed"`
	Prefilled int    `json:"prefilled"`
	LastAt    string `json:"last_at,omitempty"`
}

// Totals counts the ledger without asking GitHub anything.
func Totals(entries []Entry) LedgerTotals {
	totals := LedgerTotals{Total: len(entries)}
	for _, entry := range entries {
		if entry.Channel == "gh" {
			totals.Filed++
		} else {
			totals.Prefilled++
		}
		if entry.At > totals.LastAt {
			totals.LastAt = entry.At
		}
	}
	return totals
}

// ReportSummary is the local count plus the GitHub outcome count. The two
// halves are kept separate so an unavailable gh does not read as "0 open".
type ReportSummary struct {
	LedgerTotals
	Open       int `json:"open"`
	Resolved   int `json:"resolved"`
	NotPlanned int `json:"not_planned"`
	Unknown    int `json:"unknown"`
}

// Summarize counts outcomes. outcomes may be shorter than entries only when a
// caller passes nil, which means "not looked up": every state count stays zero.
func Summarize(entries []Entry, outcomes []Outcome) ReportSummary {
	summary := ReportSummary{LedgerTotals: Totals(entries)}
	for _, outcome := range outcomes {
		switch outcome.Status {
		case OutcomeOpen:
			summary.Open++
		case OutcomeResolved:
			summary.Resolved++
		case OutcomeNotPlanned:
			summary.NotPlanned++
		case OutcomeUnknown:
			summary.Unknown++
		}
	}
	return summary
}

// LogEntry is one ledger record plus what GitHub says about it.
type LogEntry struct {
	Entry
	Outcome
}

// LogEntries pairs the ledger with the lookup in one place, so the human view
// and the JSON cannot disagree about an entry.
func LogEntries(entries []Entry, lookup IssueLookup) []LogEntry {
	logged := make([]LogEntry, len(entries))
	for i, entry := range entries {
		logged[i] = LogEntry{Entry: entry, Outcome: OutcomeFor(entry, lookup)}
	}
	return logged
}

// ReasonText is a short sentence for an unknown outcome, bounded so a provider
// error can never paste a whole page into the shell.
func (o Outcome) ReasonText() string {
	if o.Reason == "" {
		return "state unknown"
	}
	return fmt.Sprintf("state unknown (%s)", o.Reason)
}
