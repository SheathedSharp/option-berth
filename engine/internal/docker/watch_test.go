package docker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// scriptedDocker stands in for `docker ps`: it answers with whatever the test
// last set, and counts how often it was asked.
type scriptedDocker struct {
	mu         sync.Mutex
	psCalls    int
	statsCalls int
	running    []container
	failure    error
}

func (d *scriptedDocker) list(context.Context) ([]container, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.psCalls++
	return d.running, d.failure
}

// answer replaces what the next `docker ps` will report.
func (d *scriptedDocker) answer(running []container, failure error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running, d.failure = running, failure
}

func (d *scriptedDocker) psCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.psCalls
}

// manualClock is moved by hand, so a backoff schedule can be walked without
// waiting for it.
type manualClock struct{ at time.Time }

func (c *manualClock) now() time.Time           { return c.at }
func (c *manualClock) advance(by time.Duration) { c.at = c.at.Add(by) }

// bench wires a Watcher to a scripted docker, a manual clock and a captured
// debug log. Each pass does what one iteration of Run does: refresh if due.
type bench struct {
	watcher *Watcher
	docker  *scriptedDocker
	clock   *manualClock
	log     *bytes.Buffer
	changes int
}

func newBench(t *testing.T) *bench {
	t.Helper()
	b := &bench{
		docker: &scriptedDocker{},
		clock:  &manualClock{at: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)},
		log:    &bytes.Buffer{},
	}
	logger := slog.New(slog.NewTextHandler(b.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b.watcher = NewWatcher(WatcherOptions{
		Interval: 2 * time.Second,
		Logger:   logger,
		OnChange: func() { b.changes++ },
		Now:      b.clock.now,
	})
	b.watcher.lookPath = func(string) (string, error) { return "/usr/local/bin/docker", nil }
	b.watcher.list = b.docker.list
	b.watcher.stats = func(running []container) map[string]*ContainerStats {
		b.docker.statsCalls++
		collected := map[string]*ContainerStats{}
		for _, c := range running {
			collected[c.name] = &ContainerStats{CPUPercent: 1.5, State: "running"}
		}
		return collected
	}
	return b
}

// step runs one refresh when one is due, and reports whether it did.
func (b *bench) step() bool {
	if !b.watcher.shouldRefresh() {
		return false
	}
	b.watcher.refresh(context.Background())
	return true
}

// assertQuietDuringBackoff walks the clock up to, but not including, the
// backoff for this failure. Every simulated scan may enrich from the cache;
// none may cause another docker list.
func (b *bench) assertQuietDuringBackoff(t *testing.T, failures int) {
	t.Helper()
	wait := backoffFor(failures)
	for elapsed := time.Second; elapsed < wait; elapsed += time.Second {
		b.clock.advance(time.Second)
		b.watcher.Enrich([]ports.ListeningPort{{Port: 9000 + int(elapsed/time.Second)}})
		if b.step() {
			t.Fatalf("after failure %d a refresh ran %s in, before the %s backoff", failures, elapsed, wait)
		}
	}
}

// web is the container most cases use: it publishes exactly one host port and
// carries the full set of compose labels, so a single fixture exercises both
// the port mapping and the attribution path.
var web = container{
	name:  "shop-web-1",
	image: "nginx:1.27",
	portMappings: []portMapping{{
		hostPort: 8080,
		toPort:   80,
	}},
	composeService:    "web",
	composeProject:    "shop",
	composeWorkingDir: "/src/shop",
}

// TestBackoffScheduleDoublesToTheCap pins the whole schedule, not just its
// shape: a change to MinBackoff or MaxBackoff should fail here rather than
// silently alter how often a wedged docker is retried.
func TestBackoffScheduleDoublesToTheCap(t *testing.T) {
	schedule := []time.Duration{
		MinBackoff,      // 5s
		2 * MinBackoff,  // 10s
		4 * MinBackoff,  // 20s
		8 * MinBackoff,  // 40s
		16 * MinBackoff, // 80s
		32 * MinBackoff, // 160s
		MaxBackoff,      // 5m, the sixth step already reaches it
		MaxBackoff,      // and it stays there
	}
	for attempt, expected := range schedule {
		if actual := backoffFor(attempt + 1); actual != expected {
			t.Errorf("backoffFor(%d) = %s, want %s", attempt+1, actual, expected)
		}
	}
	if far := backoffFor(1000); far != MaxBackoff {
		t.Errorf("backoffFor(1000) = %s, want the cap %s", far, MaxBackoff)
	}
}

// A failing Docker is asked again only once each backoff step has passed,
// however often scans poke, and one success resets the schedule.
func TestFailingDockerBacksOffAndASuccessResets(t *testing.T) {
	b := newBench(t)
	b.docker.answer(nil, errors.New("Cannot connect to the Docker daemon"))

	if !b.step() {
		t.Fatal("the first refresh did not run")
	}
	for failures := 1; failures <= 8; failures++ {
		b.assertQuietDuringBackoff(t, failures)
		b.clock.advance(time.Second)
		if !b.step() {
			t.Fatalf("after failure %d no refresh ran once the %s backoff was up", failures, backoffFor(failures))
		}
	}
	if got := b.docker.psCount(); got != 9 {
		t.Errorf("docker ps ran %d times, want 9", got)
	}

	b.docker.answer([]container{web}, nil)
	b.clock.advance(MaxBackoff)
	if !b.step() {
		t.Fatal("no refresh after the cap")
	}
	if !b.watcher.Healthy() {
		t.Error("a successful docker ps did not mark the watcher healthy")
	}
	// Reset: the next refresh is one interval away, not a backoff step.
	b.clock.advance(2 * time.Second)
	if !b.step() {
		t.Error("after a success the watcher still waited out a backoff")
	}
}

// While Docker fails, scans keep enriching from the last list that answered.
func TestFailingDockerServesTheLastGoodList(t *testing.T) {
	b := newBench(t)
	b.docker.answer([]container{web}, nil)
	b.step()

	b.docker.answer(nil, errors.New("boom"))
	b.clock.advance(2 * time.Second)
	b.step()
	if b.watcher.Healthy() {
		t.Fatal("a failed docker ps left the watcher healthy")
	}

	pp := []ports.ListeningPort{{Port: 8080, Type: ports.PortTypeUser}}
	b.watcher.Enrich(pp)
	requireAttributedTo(t, pp[0], web)
}

func requireAttributedTo(t *testing.T, row ports.ListeningPort, c container) {
	t.Helper()
	if row.Type != ports.PortTypeDocker {
		t.Errorf("type = %q, want %q", row.Type, ports.PortTypeDocker)
	}
	if row.DockerContainer != c.name {
		t.Errorf("container = %q, want %q", row.DockerContainer, c.name)
	}
	if row.DockerComposeProject != c.composeProject {
		t.Errorf("compose project = %q, want %q", row.DockerComposeProject, c.composeProject)
	}
	if row.DockerComposeWorkingDir != c.composeWorkingDir {
		t.Errorf("compose working dir = %q, want %q", row.DockerComposeWorkingDir, c.composeWorkingDir)
	}
}

// When docker is missing from PATH the watcher must never exec, never start a
// backoff, and say so only once.
func TestNoDockerInstalledStaysSilentAndCheap(t *testing.T) {
	b := newBench(t)
	b.watcher.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	for i := 0; i < 50; i++ {
		b.watcher.Enrich([]ports.ListeningPort{{Port: 3000 + i}})
		b.step()
		b.clock.advance(2 * time.Second)
	}
	if got := b.docker.psCount(); got != 0 {
		t.Errorf("docker ps ran %d times with no docker installed", got)
	}
	if !b.watcher.retryAt.IsZero() || b.watcher.failures != 0 {
		t.Errorf("not installed was treated as a failure: failures=%d retryAt=%s", b.watcher.failures, b.watcher.retryAt)
	}
	if n := strings.Count(b.log.String(), "\n"); n != 1 {
		t.Errorf("logged %d lines, want exactly one:\n%s", n, b.log.String())
	}
}

// The debug log names the transitions, not every tick.
func TestLogsOnlyOnHealthChanges(t *testing.T) {
	b := newBench(t)
	b.docker.answer([]container{web}, nil)
	b.step()
	b.clock.advance(2 * time.Second)
	b.step()
	if b.log.Len() != 0 {
		t.Fatalf("healthy refreshes logged:\n%s", b.log.String())
	}

	b.docker.answer(nil, errors.New("`docker ps` did not answer within 5s"))
	for i := 0; i < 4; i++ {
		b.clock.advance(MaxBackoff)
		b.step()
	}
	out := b.log.String()
	if n := strings.Count(out, "docker is not answering"); n != 1 {
		t.Errorf("logged the failure %d times, want once:\n%s", n, out)
	}
	if !strings.Contains(out, "did not answer within 5s") {
		t.Errorf("the failure line does not carry the error:\n%s", out)
	}

	b.docker.answer([]container{web}, nil)
	b.clock.advance(MaxBackoff)
	b.step()
	b.clock.advance(2 * time.Second)
	b.step()
	if n := strings.Count(b.log.String(), "docker is answering again"); n != 1 {
		t.Errorf("logged the recovery %d times, want once:\n%s", n, b.log.String())
	}
}

// Routine refreshes are rate-limited to the interval; a port nobody knew about
// asks for one early, a port already known or already published does not.
func TestANewUnattributedPortAsksForAnEarlyRefresh(t *testing.T) {
	b := newBench(t)
	b.docker.answer([]container{web}, nil)
	b.watcher.Enrich([]ports.ListeningPort{{Port: 3000}, {Port: 8080}})
	b.step()

	b.clock.advance(500 * time.Millisecond)
	b.watcher.Enrich([]ports.ListeningPort{{Port: 3000}, {Port: 8080}})
	if b.step() {
		t.Error("an unchanged port set refreshed before the interval")
	}

	b.clock.advance(100 * time.Millisecond)
	b.watcher.Enrich([]ports.ListeningPort{{Port: 3000}, {Port: 8080}, {Port: 5432}})
	if !b.step() {
		t.Error("a new port no container publishes did not ask for an early refresh")
	}

	// A new container arrives with the port: the change wakes the loop.
	before := b.changes
	db := container{name: "shop-db-1", portMappings: []portMapping{{hostPort: 5433, toPort: 5432}}}
	b.docker.answer([]container{web, db}, nil)
	b.clock.advance(100 * time.Millisecond)
	b.watcher.Enrich([]ports.ListeningPort{{Port: 3000}, {Port: 8080}, {Port: 5432}, {Port: 5433}})
	if !b.step() {
		t.Fatal("no early refresh for port 5433")
	}
	if b.changes != before+1 {
		t.Errorf("OnChange ran %d times for one changed list, want 1", b.changes-before)
	}

	// Now 5433 is published by a known container; seeing it again is not news.
	b.clock.advance(100 * time.Millisecond)
	b.watcher.Enrich([]ports.ListeningPort{{Port: 3000}, {Port: 8080}, {Port: 5432}, {Port: 5433}})
	if b.step() {
		t.Error("a known port asked for an early refresh")
	}

	// An unchanged list does not wake anyone.
	b.clock.advance(2 * time.Second)
	b.step()
	if b.changes != before+1 {
		t.Error("an unchanged list called OnChange")
	}
}

// Stats are only collected once a scan asks, and dropped while Docker fails.
func TestStatsAreCollectedOnDemandAndDroppedOnFailure(t *testing.T) {
	b := newBench(t)
	b.docker.answer([]container{web}, nil)
	b.step()
	if b.docker.statsCalls != 0 {
		t.Fatal("stats were collected before anyone asked")
	}
	if got := b.watcher.Stats(); got != nil {
		t.Errorf("stats before the first collection = %v, want nil", got)
	}
	b.clock.advance(100 * time.Millisecond)
	if !b.step() {
		t.Fatal("the first Stats call did not ask for a refresh")
	}
	st := b.watcher.Stats()
	if st["shop-web-1"] == nil || st["shop-web-1"].State != "running" {
		t.Errorf("stats = %v, want shop-web-1", st)
	}

	b.docker.answer(nil, errors.New("boom"))
	b.clock.advance(2 * time.Second)
	b.step()
	if got := b.watcher.Stats(); got != nil {
		t.Errorf("stats after a failure = %v, want none", got)
	}
}

// TestAHangingDockerNeverBlocksAScan is the reason the watcher exists: while
// `docker ps` hangs, a scan must still be able to enrich and to read stats, and
// the hanging call is cut off once its own timeout expires.
func TestAHangingDockerNeverBlocksAScan(t *testing.T) {
	watcher := NewWatcher(WatcherOptions{Interval: time.Hour})
	watcher.lookPath = func(string) (string, error) { return "/usr/local/bin/docker", nil }
	watcher.timeout = 200 * time.Millisecond

	asked := make(chan struct{}, 1)
	watcher.list = func(ctx context.Context) ([]container, error) {
		select {
		case asked <- struct{}{}:
		default:
		}
		<-ctx.Done() // block until the probe timeout ends it
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	running := make(chan struct{})
	go func() { defer close(running); watcher.Run(ctx) }()
	defer func() { cancel(); <-running }()

	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher never asked docker")
	}

	// A scan's two entry points, with the watcher's docker call still hung.
	begin := time.Now()
	for i := 0; i < 100; i++ {
		watcher.Enrich([]ports.ListeningPort{{Port: 4000 + i}})
		_ = watcher.Stats()
	}
	if took := time.Since(begin); took > 100*time.Millisecond {
		t.Errorf("100 scans' worth of Enrich took %s while docker ps hung", took)
	}

	awaitHealthDown(t, watcher)
}

// awaitHealthDown waits for the watcher to give up on a hung docker, and checks
// that it recorded the timeout as the reason.
func awaitHealthDown(t *testing.T, watcher *Watcher) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		watcher.mu.Lock()
		state, err := watcher.state, watcher.lastErr
		watcher.mu.Unlock()

		if state == healthDown {
			if err == nil || !strings.Contains(err.Error(), "did not answer within 200ms") {
				t.Errorf("lastErr = %v, want it to say the call timed out", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the hanging list call was still running after 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestParsePSFeedsEnrichment feeds a raw `docker ps` table through the
// watcher: a published port keeps the container and its compose labels, and a
// port nobody publishes stays untouched.
func TestParsePSFeedsEnrichment(t *testing.T) {
	const listed = "shop-web-1\tnginx:1.27\t0.0.0.0:8080->80/tcp, :::8080->80/tcp\tweb\tshop\t/src/shop\n" +
		"loose\tredis:7\t\t\t\t\n"
	cs := parsePS([]byte(listed))
	if len(cs) != 2 {
		t.Fatalf("parsed %d containers, want 2: %+v", len(cs), cs)
	}
	if cs[0].name != "shop-web-1" || cs[1].name != "loose" {
		t.Fatalf("parsed names = %q, %q, want shop-web-1 and loose", cs[0].name, cs[1].name)
	}
	if len(cs[1].portMappings) != 0 {
		t.Errorf("loose published %v, want no host ports", cs[1].portMappings)
	}

	b := newBench(t)
	b.docker.answer(cs, nil)
	b.step()
	pp := []ports.ListeningPort{{Port: 8080, BindAddress: "0.0.0.0"}, {Port: 8080, BindAddress: "::"}, {Port: 3000}}
	b.watcher.Enrich(pp)
	for _, row := range pp[:2] {
		if row.Type != ports.PortTypeDocker || row.DockerContainer != "shop-web-1" ||
			row.DockerImage != "nginx:1.27" || row.DockerComposeService != "web" ||
			row.DockerComposeProject != "shop" || row.DockerComposeWorkingDir != "/src/shop" ||
			row.DockerContainerPort != 80 {
			t.Errorf("port %s:%d = %+v, want the shop-web-1 attribution", row.BindAddress, row.Port, row)
		}
	}
	if pp[2].Type == ports.PortTypeDocker {
		t.Errorf("port 3000 was enriched: %+v", pp[2])
	}
}
