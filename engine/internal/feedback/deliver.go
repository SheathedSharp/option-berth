package feedback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultRepo is the only place a report is filed. It is a constant rather
	// than a setting: a report that could be pointed somewhere else would be a
	// different promise than the one docs/product.md makes.
	DefaultRepo = "SheathedSharp/option-berth"

	ghSendTimeout = 30 * time.Second
)

// The two channels a report can take.
const (
	// ChannelGH is an issue created through the person's own gh credential.
	ChannelGH = "gh"
	// ChannelURL is a prefilled page the person still has to submit.
	ChannelURL = "url"
)

// Delivery is where a report went, or where a person can send it by hand.
type Delivery struct {
	// Channel is ChannelGH when the issue was created through the user's own gh
	// login, and ChannelURL when the report is a prefilled issue page.
	Channel string
	URL     string
	// Reason says why the gh path was not used, for the line printed under
	// the URL. It never enters the report.
	Reason string
}

// ChannelFor is the channel a report takes right now, predicted from the local
// credential. It is the same predicate Deliver branches on, so `feedback
// status` cannot publish a channel the delivery path does not take.
func ChannelFor(account GHAccount) string {
	if account.AttemptsGH() {
		return ChannelGH
	}
	return ChannelURL
}

// SubmitWithGH creates the issue through the user's own GitHub account. The
// account is visible on the issue; the consent text says so.
func SubmitWithGH(ctx context.Context, repo string, report Report) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, ghSendTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "issue", "create",
		"--repo", repo,
		"--title", report.Title(),
		"--body-file", "-")
	cmd.Stdin = strings.NewReader(report.Body())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return "", fmt.Errorf("gh issue create: %w (%s)", err, detail)
		}
		return "", fmt.Errorf("gh issue create: %w", err)
	}
	link := firstURL(string(out))
	if link == "" {
		return "", errors.New("gh issue create printed no issue URL")
	}
	return link, nil
}

// IssueURL is the prefilled issue page: the fallback that needs no gh and no
// token. The person still presses submit, which is also the last review of
// what leaves the machine.
func IssueURL(repo, title, body string) string {
	query := url.Values{}
	query.Set("title", title)
	query.Set("body", body)
	return "https://github.com/" + repo + "/issues/new?" + query.Encode()
}

// Deliver files the report through gh when a credential is stored, and
// otherwise returns the prefilled URL. It never retries: one report is one
// attempt.
//
// The local probe only decides whether the attempt is worth making. Whether the
// report was actually filed is decided by `gh issue create` itself, so a
// github.com the probe could not reach still files the issue, and a failure the
// probe could not predict still falls back with the error that really happened.
func Deliver(ctx context.Context, repo string, report Report) Delivery {
	account := GHLogin(ctx)
	if !account.AttemptsGH() {
		return Delivery{
			Channel: ChannelURL,
			URL:     IssueURL(repo, report.Title(), report.Body()),
			Reason:  account.Reason,
		}
	}
	link, err := SubmitWithGH(ctx, repo, report)
	if err == nil {
		return Delivery{Channel: ChannelGH, URL: link}
	}
	return Delivery{
		Channel: ChannelURL,
		URL:     IssueURL(repo, report.Title(), report.Body()),
		Reason:  err.Error(),
	}
}

// OpenBrowser opens the prefilled page when the caller asked for it.
func OpenBrowser(ctx context.Context, link string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		command = "xdg-open"
	}
	args = append(args, link)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Run rather than Start: these launchers hand the URL to the browser and
	// exit, and the deferred cancel would otherwise kill a child that has not
	// finished handing it over.
	if err := exec.CommandContext(ctx, command, args...).Run(); err != nil {
		return fmt.Errorf("opening the issue page: %w", err)
	}
	return nil
}

func firstURL(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "https://") {
			return line
		}
	}
	return ""
}
