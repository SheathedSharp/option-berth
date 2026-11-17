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

// GHState is the typed answer to "who files the report". There are five,
// because "the probe reached github.com" and "a credential can file an issue"
// are different facts: a stored credential files an issue while the validation
// call behind it cannot reach the API.
type GHState string

const (
	// GHSignedIn: gh holds a credential for github.com and named the account.
	GHSignedIn GHState = "signed_in"
	// GHCredentialUnconfirmed: a credential for github.com is stored, but the
	// probe could not reach github.com, so the account is unconfirmed. A report
	// is still attempted through it.
	GHCredentialUnconfirmed GHState = "credential_unconfirmed"
	// GHSignedOut: gh answered and holds no credential for github.com.
	GHSignedOut GHState = "signed_out"
	// GHNotInstalled: there is no gh binary to ask.
	GHNotInstalled GHState = "not_installed"
	// GHUnknown: gh answered something that does not say whether a credential
	// exists. It is not evidence of being signed out.
	GHUnknown GHState = "unknown"
)

// GHAccount is how this installation reaches GitHub: the credential gh holds
// for github.com, or why a report cannot be attempted through it.
//
// The published `available` does not mean "the probe reached the API". It means
// the gh path is worth attempting: gh is installed and has not said there is no
// credential. An unreachable github.com does not stop `gh issue create`, so
// treating it as unavailable is what made a filed report contradict its status.
type GHAccount struct {
	State   GHState `json:"state"`
	Account string  `json:"account,omitempty"`
	Reason  string  `json:"reason,omitempty"`
}

// AttemptsGH is the one predicate the delivery path and the published channel
// share, so a report cannot take a channel its status does not name.
func (a GHAccount) AttemptsGH() bool {
	switch a.State {
	case GHSignedOut, GHNotInstalled:
		return false
	default:
		// An unconfirmed or unknown probe is a reason to try, not a reason to
		// stop: gh itself decides whether the report can be filed.
		return true
	}
}

// GHStatus is the published form of GHAccount: the state, plus the `available`
// prediction derived from it. Only Published builds one, so the JSON cannot
// disagree with the delivery path.
type GHStatus struct {
	Available bool    `json:"available"`
	State     GHState `json:"state"`
	Account   string  `json:"account,omitempty"`
	Reason    string  `json:"reason,omitempty"`
}

// Published is how the account appears in `--json`.
func (a GHAccount) Published() GHStatus {
	return GHStatus{Available: a.AttemptsGH(), State: a.State, Account: a.Account, Reason: a.Reason}
}

// GHLogin reads the local gh credential. It never sends the report, creates
// nothing and does not use a token any other component supplied.
//
// The status document is read even when gh exits non-zero: a signed-out gh
// still prints the hosts it knows, and that document is the only thing that may
// be reported as signed out.
func GHLogin(ctx context.Context) GHAccount {
	out, err := ghAuth(ctx, "auth", "status", "--json", "hosts")
	if hosts, known := parseGHHosts(out); known {
		return githubAccount(hosts)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return GHAccount{State: GHNotInstalled, Reason: "gh is not installed"}
	}
	// An older gh without --json still answers the one question that matters
	// with its exit status: a credential is present. It cannot name the
	// account, which is why the structured form is tried first.
	if _, fallbackErr := ghAuth(ctx, "auth", "status"); fallbackErr == nil {
		return GHAccount{State: GHSignedIn}
	}
	return GHAccount{State: GHUnknown, Reason: ghReason(err)}
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
	case err == nil:
		return "gh printed no account document"
	default:
		return "gh could not report its sign-in state"
	}
}

type ghAuthHosts struct {
	Hosts map[string][]ghAuthHost `json:"hosts"`
}

type ghAuthHost struct {
	State       string `json:"state"`
	Active      bool   `json:"active"`
	Login       string `json:"login"`
	TokenSource string `json:"tokenSource"`
	Error       string `json:"error"`
}

// credential names the stored credential behind this entry, or "" when there is
// none. gh reports both where the token came from and, when it could reach
// github.com, who it belongs to; an environment token has a source but no login.
func (h ghAuthHost) credential() string {
	if login := strings.TrimSpace(h.Login); login != "" {
		return login
	}
	return strings.TrimSpace(h.TokenSource)
}

// parseGHHosts reads the `--json hosts` document. known is false when gh did
// not print one, which is how a caller tells an old gh from a signed-out one.
func parseGHHosts(out []byte) (map[string][]ghAuthHost, bool) {
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, false
	}
	var decoded ghAuthHosts
	if err := json.Unmarshal(out, &decoded); err != nil || decoded.Hosts == nil {
		return nil, false
	}
	return decoded.Hosts, true
}

// githubAccount turns that document into the typed answer. The active entry is
// the credential gh would use, so it is looked at before the others.
func githubAccount(hosts map[string][]ghAuthHost) GHAccount {
	entries := hosts["github.com"]
	if len(entries) == 0 {
		return GHAccount{State: GHSignedOut, Reason: "gh is not signed in to github.com"}
	}
	ordered := make([]ghAuthHost, 0, len(entries))
	for _, entry := range entries {
		if entry.Active {
			ordered = append(ordered, entry)
		}
	}
	for _, entry := range entries {
		if !entry.Active {
			ordered = append(ordered, entry)
		}
	}
	for _, entry := range ordered {
		if entry.State == "success" && entry.credential() != "" {
			return GHAccount{State: GHSignedIn, Account: strings.TrimSpace(entry.Login)}
		}
	}
	for _, entry := range ordered {
		if entry.credential() != "" {
			return GHAccount{
				State:   GHCredentialUnconfirmed,
				Account: strings.TrimSpace(entry.Login),
				Reason:  unreachableReason(entry.Error),
			}
		}
	}
	return GHAccount{State: GHSignedOut, Reason: "gh is not signed in to github.com"}
}

// unreachableReason is the sentence for a credential the probe could not
// confirm. The gh error is bounded to one clause so a provider message cannot
// paste a whole page into the status line.
func unreachableReason(detail string) string {
	const clause = "; the stored credential is still used when filing"
	if detail = oneLine(detail, 120); detail == "" {
		return "github.com is unreachable" + clause
	}
	return "github.com is unreachable (" + detail + ")" + clause
}

// oneLine keeps the first line of a tool's message and caps it, counting runes
// so a multi-byte message is never cut in half.
func oneLine(text string, limit int) string {
	text = strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
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
	if !account.AttemptsGH() {
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
	if entry.Channel != ChannelGH {
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
		if entry.Channel != ChannelGH {
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
		if entry.Channel == ChannelGH {
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
