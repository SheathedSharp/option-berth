package feedback

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sampleReport(t *testing.T) Report {
	t.Helper()
	report, err := Build(Input{
		Kind: "stalled", Command: "up", Version: "v2.0.0",
		Platform: "linux/amd64", Services: -1,
		Now: time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return report
}

func TestIssueURLRoundTripsTheReport(t *testing.T) {
	report := sampleReport(t)
	link := IssueURL(DefaultRepo, report.Title(), report.Body())

	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.Host != "github.com" || parsed.Path != "/SheathedSharp/option-berth/issues/new" {
		t.Fatalf("url = %s", link)
	}
	query := parsed.Query()
	if query.Get("title") != report.Title() {
		t.Fatalf("title = %q, want %q", query.Get("title"), report.Title())
	}
	if query.Get("body") != report.Body() {
		t.Fatalf("body did not round trip:\n%q", query.Get("body"))
	}
	if strings.Contains(link, "\n") {
		t.Fatal("the URL carries a raw newline")
	}
}

func TestFirstURLSkipsProgressLines(t *testing.T) {
	out := "Creating issue in SheathedSharp/option-berth\nhttps://github.com/SheathedSharp/option-berth/issues/7\n"
	if got := firstURL(out); got != "https://github.com/SheathedSharp/option-berth/issues/7" {
		t.Fatalf("firstURL = %q", got)
	}
	if got := firstURL("no link here\n"); got != "" {
		t.Fatalf("firstURL on progress only = %q", got)
	}
}

// fakeGH puts an executable `gh` on PATH that records its arguments and stdin
// and prints one URL. It never touches the network.
func fakeGH(t *testing.T, exitCode int) (recordDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + record + ".args\n" +
		"/bin/cat > " + record + ".body\n" +
		"echo https://github.com/SheathedSharp/option-berth/issues/42\n" +
		"exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return dir
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}

// fakeGHScript puts an executable gh on PATH that runs script and appends the
// arguments of every call to a record file. It never touches the network.
func fakeGHScript(t *testing.T, script string) (recordPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "calls")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + recordPath + "\n" + script
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return recordPath
}

// ghUnreachable is the answer issue #356 saw: a stored credential, and a probe
// that could not reach github.com.
const ghUnreachable = `case "$1 $2" in
  "auth status")
    printf '%s\n' '{"hosts":{"github.com":[{"state":"error","active":true,"host":"github.com","login":"zayJu","tokenSource":"keyring","error":"Post \"https://api.github.com/graphql\": dial tcp: i/o timeout"}]}}'
    exit 0
    ;;
esac
`

// Issue #356: an auth probe that cannot reach github.com must not decide the
// delivery path. gh is installed and holds a credential, so the report is
// attempted, and only an actual create failure sends the person to the page.
func TestDeliverAttemptsGHWhenTheProbeCannotReachGitHub(t *testing.T) {
	recordPath := fakeGHScript(t, ghUnreachable+
		"/bin/cat >/dev/null\n"+
		"echo https://github.com/SheathedSharp/option-berth/issues/42\n")
	delivery := Deliver(context.Background(), DefaultRepo, sampleReport(t))
	if delivery.Channel != "gh" || delivery.URL != "https://github.com/SheathedSharp/option-berth/issues/42" {
		t.Fatalf("delivery = %+v, want the issue gh created", delivery)
	}
	// The published channel is the one delivery took, from the same predicate.
	if channel := ChannelFor(GHLogin(context.Background())); channel != delivery.Channel {
		t.Fatalf("published channel = %q, delivery channel = %q", channel, delivery.Channel)
	}
	calls, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "issue create") {
		t.Fatalf("gh was not asked to create the issue:\n%s", calls)
	}
}

// The fallback explains itself with the failure that actually happened, not
// with a guess about why gh might not work.
func TestDeliverReportsTheRealCreateFailure(t *testing.T) {
	fakeGHScript(t, ghUnreachable+
		"/bin/cat >/dev/null\n"+
		"echo 'HTTP 401: Bad credentials' >&2\n"+
		"exit 1\n")
	delivery := Deliver(context.Background(), DefaultRepo, sampleReport(t))
	if delivery.Channel != "url" {
		t.Fatalf("channel = %q, want url after a failed create", delivery.Channel)
	}
	if !strings.Contains(delivery.Reason, "401") {
		t.Fatalf("reason = %q, want the create failure", delivery.Reason)
	}
	if !strings.HasPrefix(delivery.URL, "https://github.com/"+DefaultRepo+"/issues/new?") {
		t.Fatalf("url = %q", delivery.URL)
	}
}

func TestGHLoginReadsTheLocalLogin(t *testing.T) {
	dir := fakeGH(t, 0)
	account := GHLogin(context.Background())
	if !account.AttemptsGH() || account.State != GHSignedIn {
		t.Fatalf("GHLogin = %+v with a signed-in gh", account)
	}
	if channel := ChannelFor(account); channel != ChannelGH {
		t.Fatalf("ChannelFor = %q, want %q", channel, ChannelGH)
	}

	t.Setenv("PATH", filepath.Join(dir, "nothing"))
	account = GHLogin(context.Background())
	if account.AttemptsGH() || account.State != GHNotInstalled || account.Reason == "" {
		t.Fatalf("GHLogin = %+v with no gh on PATH", account)
	}
	if channel := ChannelFor(account); channel != ChannelURL {
		t.Fatalf("ChannelFor = %q, want %q", channel, ChannelURL)
	}
}

func TestSubmitWithGHPassesTheBodyOnStdin(t *testing.T) {
	dir := fakeGH(t, 0)
	report := sampleReport(t)
	link, err := SubmitWithGH(context.Background(), DefaultRepo, report)
	if err != nil {
		t.Fatalf("SubmitWithGH: %v", err)
	}
	if link != "https://github.com/SheathedSharp/option-berth/issues/42" {
		t.Fatalf("link = %q", link)
	}
	args, err := os.ReadFile(filepath.Join(dir, "record.args"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"issue", "create", "--repo", DefaultRepo, "--title", report.Title(), "--body-file", "-"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("gh args are missing %q:\n%s", want, args)
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, "record.body"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != report.Body() {
		t.Fatalf("gh stdin did not carry the report body:\n%s", body)
	}
}

func TestDeliverFallsBackToThePrefilledPage(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	report := sampleReport(t)
	delivery := Deliver(context.Background(), DefaultRepo, report)
	if delivery.Channel != "url" {
		t.Fatalf("channel = %q, want url when gh is absent", delivery.Channel)
	}
	if delivery.Reason == "" {
		t.Fatal("the fallback must say why gh was not used")
	}
	if !strings.HasPrefix(delivery.URL, "https://github.com/"+DefaultRepo+"/issues/new?") {
		t.Fatalf("url = %q", delivery.URL)
	}
}

func TestDeliverUsesGHWhenItAnswers(t *testing.T) {
	fakeGH(t, 0)
	delivery := Deliver(context.Background(), DefaultRepo, sampleReport(t))
	if delivery.Channel != "gh" || delivery.URL != "https://github.com/SheathedSharp/option-berth/issues/42" {
		t.Fatalf("delivery = %+v", delivery)
	}
}
