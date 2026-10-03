package scanner

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// healthServer starts a real HTTP listener on loopback and returns it with its
// port. A synthetic listener is the only honest way to test a probe: the thing
// under test is an HTTP round trip.
func healthServer(t *testing.T, status int) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
	}))
	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return srv, port
}

// healthLoop wires a loop over one fake row pointing at the live server, with
// a `.oberth.yaml` in dir that gives the service a health path.
func healthLoop(t *testing.T, dir string, port int) (*Loop, *[]state.Event, *sync.Mutex) {
	t.Helper()
	dir = resolved(t, dir)
	writeConfig(t, dir, "name: probed\nservices:\n  - name: api\n    port: "+
		strconv.Itoa(port)+"\n    health: /healthz\n")

	var mu sync.Mutex
	var seen []state.Event

	l := New(Options{
		DaemonVersion: "test",
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{
				Port: port, BindAddress: "127.0.0.1", IPVersion: "ipv4",
				PID: 4242, Process: "python3", Command: "python3 -m http.server", Cwd: dir,
			}}, nil
		},
		Publish: func(_, _ state.Snapshot, events []state.Event) {
			mu.Lock()
			seen = append(seen, events...)
			mu.Unlock()
		},
	})
	return l, &seen, &mu
}

// TestConfiguredHealthIsPolledWithoutAsking is the step's promise: a `health:`
// path in a `.oberth.yaml` is polled on every tick and the result is on the port
// row, with no `include: ["health"]` from anybody.
func TestConfiguredHealthIsPolledWithoutAsking(t *testing.T) {
	srv, port := healthServer(t, http.StatusOK)
	defer srv.Close()

	l, _, _ := healthLoop(t, t.TempDir(), port)

	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Ports) != 1 {
		t.Fatalf("ports = %+v", snap.Ports)
	}
	h := snap.Ports[0].Health
	if h == nil {
		t.Fatal("no health on a port whose service declares a health path")
	}
	if h.Status != state.HealthOK || h.Code != http.StatusOK {
		t.Fatalf("health = %+v, want ok/200", h)
	}
	if !h.Configured {
		t.Error("the probe is not marked as configured, so include would strip it")
	}
	if len(snap.Groups) != 1 || len(snap.Groups[0].Services) != 1 {
		t.Fatalf("groups = %+v, want one service row", snap.Groups)
	}
	serviceHealth := snap.Groups[0].Services[0].HealthStatus
	if serviceHealth == nil || serviceHealth.Status != state.HealthOK || serviceHealth.Code != http.StatusOK {
		t.Fatalf("service health = %+v, want the configured port verdict", serviceHealth)
	}
}

func TestSyncConfiguredHealthPublishesTheServiceVerdict(t *testing.T) {
	port := 18090
	rows := []state.Port{{Port: port, Health: &state.Health{
		Status: state.HealthFail, Code: 503, Reason: "http_503", Configured: true,
	}}}
	groups := []state.Group{{Services: []state.Service{{
		Name: "api", PortActual: &port,
	}}}}
	syncConfiguredHealth(rows, groups)
	if got := groups[0].Services[0].HealthStatus; got == nil || got.Status != state.HealthFail || got.Code != 503 {
		t.Fatalf("service health = %+v, want port verdict", got)
	}
}

// TestConfiguredHealthBudgetBoundsTheRound answers the plan's "does a health
// probe block state publication": with probes that honor the deadline they are
// handed, one round costs at most its budget however many black-hole endpoints
// a project declares. A probe that starts with no budget left is skipped and
// the row keeps its previous verdict (carryHealth) rather than flickering to
// unknown.
func TestConfiguredHealthBudgetBoundsTheRound(t *testing.T) {
	const budget = 80 * time.Millisecond
	healthPath := "/healthz"
	var rows []state.Port
	var services []state.Service
	for i, port := range []int{18091, 18092, 18093} {
		p := port
		rows = append(rows, state.Port{Port: p, BindAddress: "127.0.0.1"})
		services = append(services, state.Service{
			Name: fmt.Sprintf("svc-%d", i), PortActual: &p, Health: &healthPath,
		})
	}
	groups := []state.Group{{Services: services}}

	var probed int32
	probe := func(host string, port int, path string, timeout time.Duration) ports.HealthResult {
		atomic.AddInt32(&probed, 1)
		time.Sleep(timeout) // an endpoint that accepts and never answers
		return ports.HealthResult{Status: "timeout", ObservedAt: time.Now().UTC()}
	}

	start := time.Now()
	probeConfigured(rows, groups, probe, budget)
	elapsed := time.Since(start)
	if probed == 0 {
		t.Fatal("no probe ran")
	}
	if elapsed > budget+700*time.Millisecond {
		t.Fatalf("a %v budget held the round for %v", budget, elapsed)
	}
	for i := range rows {
		if rows[i].Health == nil || !rows[i].Health.Configured || rows[i].Health.Status != state.HealthFail {
			t.Fatalf("row %d health = %+v, want a configured failure verdict", i, rows[i].Health)
		}
	}

	// Budget already spent: nothing starts, and the prior verdict stays.
	port := 18094
	prior := &state.Health{Status: state.HealthOK, Configured: true}
	spentRows := []state.Port{{Port: port, Health: prior}}
	spent := []state.Group{{Services: []state.Service{{
		Name: "svc", PortActual: &port, Health: &healthPath,
	}}}}
	var started int32
	probeConfigured(spentRows, spent, func(string, int, string, time.Duration) ports.HealthResult {
		atomic.AddInt32(&started, 1)
		return ports.HealthResult{}
	}, time.Nanosecond)
	if started != 0 {
		t.Fatalf("%d probes started after the budget was spent", started)
	}
	if spentRows[0].Health != prior {
		t.Fatalf("a skipped probe replaced the prior verdict: %+v", spentRows[0].Health)
	}
}

// TestConfiguredHealthFailsWhenTheServerGoesAway, and the transition is
// reported as a health_changed event.
func TestConfiguredHealthFailsWhenTheServerGoesAway(t *testing.T) {
	srv, port := healthServer(t, http.StatusOK)
	l, seen, mu := healthLoop(t, t.TempDir(), port)

	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}

	srv.Close()
	l.Invalidate()

	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	h := snap.Ports[0].Health
	if h == nil || h.Status != state.HealthFail {
		t.Fatalf("health after the server closed = %+v, want fail", h)
	}
	if h.Reason == "" {
		t.Error("a failing probe should say why")
	}

	mu.Lock()
	defer mu.Unlock()
	var changed *state.Event
	for i := range *seen {
		if (*seen)[i].Kind == "health_changed" {
			changed = &(*seen)[i]
		}
	}
	if changed == nil {
		t.Fatalf("no health_changed event in %+v", *seen)
	}
	if changed.Data["from"] != state.HealthOK || changed.Data["to"] != state.HealthFail {
		t.Errorf("health_changed data = %+v, want ok -> fail", changed.Data)
	}
}

// TestConfiguredHealthReportsANonOKCode as a failure with the code intact, so a
// client can show "503" rather than just "fail".
func TestConfiguredHealthReportsANonOKCode(t *testing.T) {
	srv, port := healthServer(t, http.StatusServiceUnavailable)
	defer srv.Close()

	l, _, _ := healthLoop(t, t.TempDir(), port)
	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	h := snap.Ports[0].Health
	if h == nil || h.Status != state.HealthFail || h.Code != http.StatusServiceUnavailable {
		t.Fatalf("health = %+v, want fail/503", h)
	}
}

// TestNoHealthPathMeansNoProbe: a service without `health:` leaves the row's
// health null, so an unconfigured project pays nothing for this feature.
func TestNoHealthPathMeansNoProbe(t *testing.T) {
	srv, port := healthServer(t, http.StatusOK)
	defer srv.Close()

	dir := resolved(t, t.TempDir())
	writeConfig(t, dir, "name: plain\nservices:\n  - name: api\n    port: "+strconv.Itoa(port)+"\n")

	l := New(Options{
		DaemonVersion: "test",
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{
				Port: port, BindAddress: "127.0.0.1", PID: 4242, Process: "python3", Cwd: dir,
			}}, nil
		},
	})
	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Ports[0].Health != nil {
		t.Fatalf("health = %+v, want null with no health path configured", snap.Ports[0].Health)
	}
}

// TestPreferHostPicksIPv6OnlyWhenItHasTo.
func TestPreferHostPicksIPv6OnlyWhenItHasTo(t *testing.T) {
	v6 := state.Port{BindAddress: "::1"}
	v4 := state.Port{BindAddress: "127.0.0.1"}

	if got := preferHost("", v6); got != "[::1]" {
		t.Errorf("an IPv6-only listener probes %q, want [::1]", got)
	}
	if got := preferHost(preferHost("", v6), v4); got != "127.0.0.1" {
		t.Errorf("a dual-stack listener probes %q, want 127.0.0.1", got)
	}
	if got := preferHost(preferHost("", v4), v6); got != "127.0.0.1" {
		t.Errorf("IPv4 should win regardless of row order, got %q", got)
	}
}
