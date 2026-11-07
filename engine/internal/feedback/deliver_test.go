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

func TestGHAvailableReadsTheLocalLogin(t *testing.T) {
	dir := fakeGH(t, 0)
	if ok, reason := GHAvailable(context.Background()); !ok {
		t.Fatalf("GHAvailable = false (%s) with a signed-in gh", reason)
	}

	t.Setenv("PATH", filepath.Join(dir, "nothing"))
	if ok, reason := GHAvailable(context.Background()); ok || reason == "" {
		t.Fatalf("GHAvailable = (%v, %q) with no gh on PATH", ok, reason)
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
