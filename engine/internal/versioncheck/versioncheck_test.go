package versioncheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareReleaseTags(t *testing.T) {
	for _, test := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v1.1.2", "v1.1.2", 0, true},
		{"v1.1.2", "v1.1.3", -1, true},
		{"v1.2.0", "v1.1.9", 1, true},
		{"v2.0.0", "v1.99.99", 1, true},
		{"1.1.2", "v1.1.2", 0, true},
		{"v1.1", "v1.1.2", 0, false},
		{"dev", "v1.1.2", 0, false},
		{"v1.1.2-rc1", "v1.1.2", 0, false},
		{"v1.1.2", "nightly", 0, false},
	} {
		got, ok := Compare(test.a, test.b)
		if got != test.want || ok != test.ok {
			t.Errorf("Compare(%q, %q) = (%d, %v), want (%d, %v)", test.a, test.b, got, ok, test.want, test.ok)
		}
	}
}

func TestRelate(t *testing.T) {
	for _, test := range []struct {
		current, latest string
		want            Relation
	}{
		{"v1.1.2", "v1.1.2", Same},
		{"v1.1.2", "v1.2.0", Behind},
		{"v1.2.0", "v1.1.2", Ahead},
		{"dev", "v1.1.2", Unknown},
		{"v1.1.2", "", Unknown},
	} {
		if got := Relate(test.current, test.latest); got != test.want {
			t.Errorf("Relate(%q, %q) = %v, want %v", test.current, test.latest, got, test.want)
		}
	}
}

func TestReleasePageSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			http.Redirect(w, r, "/releases/tag/v1.2.3", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tag, err := newTestChecker(t, server.URL).fromReleasePage(context.Background())
	if err != nil {
		t.Fatalf("fromReleasePage: %v", err)
	}
	if tag != "v1.2.3" {
		t.Fatalf("tag = %q, want v1.2.3", tag)
	}
}

func TestReleasePageRejectsNonReleaseTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/nightly", http.StatusFound)
	}))
	defer server.Close()

	if _, err := newTestChecker(t, server.URL).fromReleasePage(context.Background()); err == nil {
		t.Fatal("fromReleasePage accepted a non-release tag")
	}
}

func TestAPISource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.2.3"})
	}))
	defer server.Close()

	tag, err := newTestChecker(t, server.URL).fromAPI(context.Background())
	if err != nil {
		t.Fatalf("fromAPI: %v", err)
	}
	if tag != "v1.2.3" {
		t.Fatalf("tag = %q, want v1.2.3", tag)
	}
}

func TestAPIRejectsNonReleaseTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "nightly"})
	}))
	defer server.Close()

	if _, err := newTestChecker(t, server.URL).fromAPI(context.Background()); err == nil {
		t.Fatal("fromAPI accepted a non-release tag")
	}
}

func TestLookupReusesFreshCache(t *testing.T) {
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	checker := newTestChecker(t, "http://127.0.0.1:1")
	checker.now = func() time.Time { return now }
	calls := 0
	checker.fetch = func(context.Context) (string, error) {
		calls++
		return "v1.1.2", nil
	}

	first, err := checker.lookup(context.Background(), "v1.1.2", false, time.Second)
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	second, err := checker.lookup(context.Background(), "v1.1.2", false, time.Second)
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fetches = %d, want the fresh cache to be reused", calls)
	}
	if first.Latest != "v1.1.2" || second.Latest != "v1.1.2" {
		t.Fatalf("states = %+v / %+v, want the cached latest", first, second)
	}
}

func TestLookupChecksAgainForAnotherBuild(t *testing.T) {
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	checker := newTestChecker(t, "http://127.0.0.1:1")
	checker.now = func() time.Time { return now }
	calls := 0
	checker.fetch = func(context.Context) (string, error) {
		calls++
		return "v1.1.2", nil
	}

	if _, err := checker.lookup(context.Background(), "v1.1.2", false, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := checker.lookup(context.Background(), "v1.2.0", false, time.Second); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("fetches = %d, want a check for the build that changed", calls)
	}
}

func TestLookupExpiresCache(t *testing.T) {
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	checker := newTestChecker(t, "http://127.0.0.1:1")
	checker.now = func() time.Time { return now }
	calls := 0
	checker.fetch = func(context.Context) (string, error) {
		calls++
		return "v1.1.2", nil
	}

	if _, err := checker.lookup(context.Background(), "v1.1.2", false, time.Second); err != nil {
		t.Fatal(err)
	}
	now = now.Add(CacheTTL + time.Second)
	if _, err := checker.lookup(context.Background(), "v1.1.2", false, time.Second); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("fetches = %d, want the expired cache to be rechecked", calls)
	}
}

func TestLookupForceSkipsCache(t *testing.T) {
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	checker := newTestChecker(t, "http://127.0.0.1:1")
	checker.now = func() time.Time { return now }
	calls := 0
	checker.fetch = func(context.Context) (string, error) {
		calls++
		return "v1.1.2", nil
	}

	if _, err := checker.lookup(context.Background(), "v1.1.2", false, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := checker.lookup(context.Background(), "v1.1.2", true, time.Second); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("fetches = %d, want --check to bypass the cache", calls)
	}
}

func TestLookupRecordsFailureAsUnknown(t *testing.T) {
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	checker := newTestChecker(t, "http://127.0.0.1:1")
	checker.now = func() time.Time { return now }
	calls := 0
	checker.fetch = func(context.Context) (string, error) {
		calls++
		return "", errors.New("network down")
	}

	state, err := checker.lookup(context.Background(), "v1.1.2", false, time.Second)
	if err == nil {
		t.Fatal("lookup returned nil error for a failed check")
	}
	if state.Latest != "" || state.Error == "" {
		t.Fatalf("state = %+v, want no latest and a recorded failure", state)
	}

	// A failed attempt is still an attempt: the next welcome must not retry
	// immediately, and it must not turn the failure into "latest".
	cached, err := checker.lookup(context.Background(), "v1.1.2", false, time.Second)
	if err != nil {
		t.Fatalf("cached lookup: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fetches = %d, want the failure to be cached", calls)
	}
	if cached.Latest != "" || cached.Error == "" {
		t.Fatalf("cached state = %+v, want the recorded failure", cached)
	}
}

func TestLookupWritesOwnerOnlyState(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("mode bits are POSIX-only")
	}
	checker := newTestChecker(t, "http://127.0.0.1:1")
	checker.fetch = func(context.Context) (string, error) { return "v1.1.2", nil }
	if _, err := checker.lookup(context.Background(), "v1.1.2", false, time.Second); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(checker.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state mode = %o, want 600", got)
	}
}

func TestFirstReleaseReturnsFirstSuccess(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	tag, err := firstRelease(context.Background(),
		func(context.Context) (string, error) {
			<-release
			return "", errors.New("blocked")
		},
		func(context.Context) (string, error) { return "v1.0.0", nil },
	)
	if err != nil {
		t.Fatalf("firstRelease: %v", err)
	}
	if tag != "v1.0.0" {
		t.Fatalf("tag = %q, want v1.0.0", tag)
	}
}

func TestFirstReleaseReportsEveryFailure(t *testing.T) {
	_, err := firstRelease(context.Background(),
		func(context.Context) (string, error) { return "", errors.New("page down") },
		func(context.Context) (string, error) { return "", errors.New("api down") },
	)
	if err == nil {
		t.Fatal("firstRelease returned nil error")
	}
	for _, want := range []string{"page down", "api down"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

func TestFirstReleasePreservesTimeoutCause(t *testing.T) {
	cause := fmt.Errorf("dial: %w", context.DeadlineExceeded)
	_, err := firstRelease(context.Background(),
		func(context.Context) (string, error) { return "", cause },
		func(context.Context) (string, error) { return "", errors.New("api down") },
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the timeout cause preserved", err)
	}
}

func TestEnabledFrom(t *testing.T) {
	for _, test := range []struct {
		name     string
		env      string
		terminal bool
		want     bool
	}{
		{"terminal", "", true, true},
		{"no terminal", "", false, false},
		{"opted out", "1", true, false},
		{"explicit zero keeps it on", "0", true, true},
		{"explicit false keeps it on", "false", true, true},
		{"arbitrary value opts out", "quiet", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := enabledFrom(func(string) string { return test.env }, test.terminal)
			if got != test.want {
				t.Fatalf("enabledFrom(%q, %v) = %v, want %v", test.env, test.terminal, got, test.want)
			}
		})
	}
}

func newTestChecker(t *testing.T, baseURL string) *checker {
	t.Helper()
	return &checker{
		client:     newClient(),
		releaseURL: baseURL + "/releases/latest",
		apiURL:     baseURL + "/api/releases/latest",
		statePath:  filepath.Join(t.TempDir(), "update-check.json"),
		now:        time.Now,
		ttl:        CacheTTL,
	}
}
