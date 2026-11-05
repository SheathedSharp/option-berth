// Package versioncheck compares this build with the latest published release.
//
// It is read-only: it reads the newest release tag from the project's release
// feed and tells the CLI whether a newer install exists. It never downloads,
// installs or replaces a binary. The welcome screen and the terminal footer
// reuse a cached answer so a command does not pay for a network round-trip
// every time; `oberth version --check` asks directly.
package versioncheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
)

const (
	// Repository is the public project the release tags belong to.
	Repository = "SheathedSharp/option-berth"

	// SuccessTTL is how long a successful answer is reused. Five minutes keeps
	// the welcome screen cheap without hiding a fresh release for a day: a
	// check made just before a release must not claim "latest" hours later.
	SuccessTTL = 5 * time.Minute

	// FailureTTL is how long a failed check is remembered. It is longer than a
	// success so an unreachable feed does not add a timeout to every run.
	FailureTTL = 30 * time.Minute

	// WelcomeTimeout bounds the check the welcome screen may run. It is short
	// because the screen is a signpost: an unreachable network must not make
	// `oberth` feel hung, and an unknown answer is better than a slow one.
	WelcomeTimeout = time.Second

	// CheckTimeout is the explicit `oberth version --check` budget. The
	// caller asked for this answer, so it may wait longer than the welcome.
	CheckTimeout = 6 * time.Second

	userAgent = "oberth-version-check"
)

var releasePattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// release is a plain X.Y.Z version. The project does not publish prereleases;
// anything else is not a release tag and must not be compared as one.
type release struct{ major, minor, patch int }

func parseRelease(tag string) (release, bool) {
	m := releasePattern.FindStringSubmatch(strings.TrimSpace(tag))
	if m == nil {
		return release{}, false
	}
	values := [3]int{}
	for i := range values {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return release{}, false
		}
		values[i] = n
	}
	return release{values[0], values[1], values[2]}, true
}

// IsRelease reports whether tag is a plain release tag (vX.Y.Z or X.Y.Z).
func IsRelease(tag string) bool {
	_, ok := parseRelease(tag)
	return ok
}

// Compare orders two release tags: -1 when a is older, 0 when they are the
// same release, and 1 when a is newer. ok is false when either side is not a
// release tag, so "dev" or a prerelease never turns into an update claim.
func Compare(a, b string) (order int, ok bool) {
	left, ok := parseRelease(a)
	if !ok {
		return 0, false
	}
	right, ok := parseRelease(b)
	if !ok {
		return 0, false
	}
	switch {
	case left.major != right.major:
		return sign(left.major - right.major), true
	case left.minor != right.minor:
		return sign(left.minor - right.minor), true
	default:
		return sign(left.patch - right.patch), true
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

// Relation says how this build relates to the latest release.
type Relation int

const (
	// Unknown means either side is not a comparable release, or no check has
	// produced a latest tag. It is never treated as "up to date".
	Unknown Relation = iota
	Same
	Behind
	Ahead
)

// Relate classifies current against latest.
func Relate(current, latest string) Relation {
	order, ok := Compare(current, latest)
	if !ok {
		return Unknown
	}
	switch {
	case order == 0:
		return Same
	case order < 0:
		return Behind
	default:
		return Ahead
	}
}

// State is one observation: the build that asked, the latest release it saw,
// and when it looked. Latest is empty when the check failed; Error says why.
// A failed check is recorded so the next welcome does not retry immediately,
// but it is never rendered as if this build were current.
type State struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
	// Notified is the latest release a terminal notice already named for this
	// build. The welcome screen and `version --check` ignore it; the automatic
	// footer reads it so one release is announced once, not on every command.
	Notified string `json:"notified,omitempty"`
}

// Relation says how the build that made this observation relates to the
// latest release it saw.
func (s State) Relation() Relation {
	return Relate(s.Current, s.Latest)
}

// UpdateAvailable reports whether a newer release is known.
func (s State) UpdateAvailable() bool {
	return s.Relation() == Behind
}

func (s State) fresh(now time.Time, ttl time.Duration) bool {
	if s.CheckedAt.IsZero() || s.CheckedAt.After(now) {
		return false
	}
	return now.Sub(s.CheckedAt) < ttl
}

// Lookup returns what is known about the latest release for current.
//
// Unless force is set, a cached answer for the same build is reused while it is
// younger than SuccessTTL (FailureTTL when the last check failed). A check
// records its result — including a failure — so
// the welcome screen can show an honest "unknown" instead of retrying on every
// invocation. A check failure is returned to the caller; the returned State is
// still written to the cache.
func Lookup(ctx context.Context, current string, force bool, timeout time.Duration) (State, error) {
	return defaultChecker().lookup(ctx, current, force, timeout)
}

// Enabled reports whether the welcome screen may run an implicit check: a
// terminal is attached and the user has not opted out. Explicit
// `version --check` does not consult this.
func Enabled() bool { return EnabledFile(os.Stdout) }

// EnabledFile reports whether an implicit check may run for output written to
// file: that stream is a terminal and the user has not opted out. The welcome
// screen asks about stdout, the update footer about stderr — the stream the
// notice would be written to is the one that has to be a terminal.
func EnabledFile(file *os.File) bool {
	info, err := file.Stat()
	terminal := err == nil && info.Mode()&os.ModeCharDevice != 0
	return enabledFrom(os.Getenv, terminal)
}

func enabledFrom(getenv func(string) string, terminal bool) bool {
	if !terminal {
		return false
	}
	// Presence disables, the way NO_COLOR works; the explicit false spellings
	// are there so a shell profile can set the switch to 0.
	switch strings.TrimSpace(strings.ToLower(getenv("BERTH_NO_UPDATE_CHECK"))) {
	case "", "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

// defaultChecker reads the public release feed and caches under BERTH_HOME,
// the one directory this program owns.
func defaultChecker() *checker {
	return &checker{
		client:     newClient(),
		releaseURL: "https://github.com/" + Repository + "/releases/latest",
		apiURL:     "https://api.github.com/repos/" + Repository + "/releases/latest",
		cdnURL:     "https://data.jsdelivr.com/v1/package/gh/" + Repository,
		statePath:  filepath.Join(paths.Dir(), paths.UpdateCheckFile),
		now:        time.Now,
		successTTL: SuccessTTL,
		failureTTL: FailureTTL,
	}
}

type checker struct {
	client     *http.Client
	releaseURL string
	apiURL     string
	cdnURL     string
	statePath  string
	now        func() time.Time
	successTTL time.Duration
	failureTTL time.Duration

	// fetch replaces the network sources in tests. Production leaves it nil.
	fetch func(context.Context) (string, error)
}

// fresh reports whether a cached state may be reused. A failure is remembered
// longer than a success: retrying an unreachable feed every few minutes would
// add its timeout to most runs.
func (c *checker) fresh(state State, now time.Time) bool {
	ttl := c.successTTL
	if state.Error != "" {
		ttl = c.failureTTL
	}
	return state.fresh(now, ttl)
}

func (c *checker) lookup(ctx context.Context, current string, force bool, timeout time.Duration) (State, error) {
	current = strings.TrimSpace(current)
	now := c.now()
	cached, cachedOK := c.read()
	if !force && cachedOK && cached.Current == current && c.fresh(cached, now) {
		return cached, nil
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	latest, err := c.latest(ctx)
	state := State{Current: current, CheckedAt: c.now()}
	if err != nil {
		state.Error = err.Error()
	} else {
		state.Latest = latest
	}
	// A check must not forget that this release was already announced: the
	// notice is once per (build, release), not once per cache refresh.
	if cachedOK && cached.Current == current {
		state.Notified = cached.Notified
	}
	_ = c.write(state)
	if err != nil {
		return state, err
	}
	return state, nil
}

// MarkNotified records that a terminal footer has named latest for current.
// The next lookup for the same build carries the marker forward, so the footer
// stays quiet until a newer release appears. The cache is advisory: losing the
// race to another process costs at most a repeated notice, never a wrong fact.
func MarkNotified(current, latest string) {
	if !IsRelease(current) || !IsRelease(latest) {
		return
	}
	defaultChecker().markNotified(current, latest)
}

func (c *checker) markNotified(current, latest string) {
	cached, ok := c.read()
	if !ok || cached.Current != current {
		return
	}
	cached.Notified = latest
	cached.CheckedAt = c.now()
	_ = c.write(cached)
}

func (c *checker) latest(ctx context.Context) (string, error) {
	if c.fetch != nil {
		return c.fetch(ctx)
	}
	return firstRelease(ctx, c.fromReleasePage, c.fromAPI, c.fromCDN)
}

// firstRelease runs every source and returns the first valid answer. All three
// endpoints are authoritative; racing them means a blocked path does not cost
// the others their turn. A source that loses the race is cancelled, and the
// buffered channel lets it finish without a goroutine leak.
func firstRelease(ctx context.Context, sources ...func(context.Context) (string, error)) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		version string
		err     error
	}
	results := make(chan result, len(sources))
	for _, source := range sources {
		go func(source func(context.Context) (string, error)) {
			version, err := source(ctx)
			results <- result{version, err}
		}(source)
	}

	var failures []error
	fail := func() error {
		messages := make([]string, len(failures))
		for i, err := range failures {
			messages[i] = oneLine(err)
		}
		return &sourceError{
			summary: "no release source answered: " + strings.Join(messages, "; "),
			causes:  failures,
		}
	}
	for range sources {
		select {
		case r := <-results:
			if r.err == nil {
				cancel()
				return r.version, nil
			}
			failures = append(failures, r.err)
		case <-ctx.Done():
			failures = append(failures, ctx.Err())
			return "", fail()
		}
	}
	return "", fail()
}

// sourceError keeps the one-line summary the CLI renders while preserving the
// causes for errors.Is, so a caller can still tell a timeout from a 404.
type sourceError struct {
	summary string
	causes  []error
}

func (e *sourceError) Error() string   { return e.summary }
func (e *sourceError) Unwrap() []error { return e.causes }

func (c *checker) fromReleasePage(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.releaseURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var location *url.URL
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		location, err = resp.Location()
		if err != nil {
			return "", fmt.Errorf("release page redirect: %w", err)
		}
	case resp.StatusCode == http.StatusOK:
		// A client that followed redirects lands on the tag page itself.
		location = resp.Request.URL
	default:
		return "", fmt.Errorf("release page returned %s", resp.Status)
	}

	tag := strings.TrimSpace(path.Base(strings.TrimRight(location.Path, "/")))
	if !IsRelease(tag) {
		return "", fmt.Errorf("release page pointed at %q, not a release tag", tag)
	}
	return tag, nil
}

func (c *checker) fromAPI(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release API returned %s", resp.Status)
	}
	var document struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&document); err != nil {
		return "", fmt.Errorf("release API response: %w", err)
	}
	tag := strings.TrimSpace(document.TagName)
	if !IsRelease(tag) {
		return "", fmt.Errorf("release API returned tag %q", document.TagName)
	}
	return tag, nil
}

// fromCDN reads jsDelivr's package index, which lists the repository's tags
// from a CDN that stays reachable where github.com is slow or blocked. The
// index holds every tag — module tags such as engine/v2.0.0 included — so the
// newest plain release is chosen here rather than trusting list order.
func (c *checker) fromCDN(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cdnURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CDN index returned %s", resp.Status)
	}
	var document struct {
		Versions []string `json:"versions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&document); err != nil {
		return "", fmt.Errorf("CDN index response: %w", err)
	}
	best := ""
	for _, version := range document.Versions {
		if !IsRelease(version) {
			continue
		}
		if best == "" {
			best = version
			continue
		}
		if order, ok := Compare(version, best); ok && order > 0 {
			best = version
		}
	}
	if best == "" {
		return "", fmt.Errorf("CDN index listed no release tag")
	}
	if !strings.HasPrefix(best, "v") {
		best = "v" + best
	}
	return best, nil
}

func (c *checker) read() (State, bool) {
	data, err := os.ReadFile(c.statePath)
	if err != nil {
		return State{}, false
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, false
	}
	return state, true
}

func (c *checker) write(state State) error {
	dir := filepath.Dir(c.statePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(dir, ".update-check-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, c.statePath)
}

func newClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// A check is one short request from a short-lived process; there is no
	// pool worth keeping alive, and a lingering connection can hold a test
	// server open.
	transport.DisableKeepAlives = true
	return &http.Client{
		Transport: transport,
		// The release page answers with the tag in Location. Following it
		// would download a whole HTML page only to parse it back out.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
