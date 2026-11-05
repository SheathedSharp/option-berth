package claims_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/claims"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// managerWithStore builds a manager over a real temporary database, so the SQL
// that enforces one holder per port runs together with the probe.
func managerWithStore(t *testing.T, listening map[int]bool, clock func() time.Time) (*claims.Manager, *store.Store) {
	t.Helper()
	st := openStore(t)
	m := claims.New(st.Claims(), claims.Options{
		Now:       clock,
		Listening: func() (map[int]bool, error) { return listening, nil },
	})
	return m, st
}

func mustAcquire(t *testing.T, m *claims.Manager, req claims.Request) claims.Result {
	t.Helper()
	result, err := m.Acquire(req)
	if err != nil {
		t.Fatalf("Acquire(%+v): %v", req, err)
	}
	return result
}

func seedClaim(t *testing.T, st *store.Store, port int, key string, expires time.Time) {
	t.Helper()
	project, worktree, _ := strings.Cut(key, "/")
	if err := st.Claims().Put(store.ClaimRow{
		Port: port, Key: key, Project: project, Worktree: worktree, ExpiresAt: expires,
	}); err != nil {
		t.Fatalf("seeding claim %d/%s: %v", port, key, err)
	}
}

func heldRows(t *testing.T, st *store.Store, key string) []store.ClaimRow {
	t.Helper()
	rows, err := st.Claims().Get(key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return rows
}

func allClaims(t *testing.T, st *store.Store) []store.ClaimRow {
	t.Helper()
	rows, err := st.Claims().List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return rows
}

// projectCounts records DefaultCount's callers and answers a fixed block for
// one project.
type projectCounts struct {
	asked []string
	block int
}

func (p *projectCounts) countFor(project string) int {
	p.asked = append(p.asked, project)
	if project == "shop" {
		return p.block
	}
	return 0
}

func TestSameKeySamePorts(t *testing.T) {
	m, _ := managerWithStore(t, nil, nil)
	first := mustAcquire(t, m, claims.Request{Project: "myapp", Worktree: "feature-x"})
	second := mustAcquire(t, m, claims.Request{Project: "myapp", Worktree: "feature-x"})

	if len(first.Ports) != 1 || first.Ports[0] != claims.DefaultRange.Base("myapp/feature-x") {
		t.Fatalf("ports = %v, want the derived base", first.Ports)
	}
	if second.Ports[0] != first.Ports[0] {
		t.Errorf("second acquire = %v, want %d", second.Ports, first.Ports[0])
	}
	if first.Key != "myapp/feature-x" {
		t.Errorf("key = %q", first.Key)
	}
}

func TestSiblingWorktreesGetDifferentPorts(t *testing.T) {
	m, _ := managerWithStore(t, nil, nil)
	x := mustAcquire(t, m, claims.Request{Project: "myapp", Worktree: "feature-x"})
	y := mustAcquire(t, m, claims.Request{Project: "myapp", Worktree: "feature-y"})
	if x.Ports[0] == y.Ports[0] {
		t.Fatalf("both worktrees received port %d", x.Ports[0])
	}
}

func TestListeningPortsAreSkipped(t *testing.T) {
	key := "myapp/main"
	base := claims.DefaultRange.Base(key)
	m, _ := managerWithStore(t, map[int]bool{base: true, base + 1: true}, nil)

	result := mustAcquire(t, m, claims.Request{Key: key})
	if result.Ports[0] != base+2 {
		t.Fatalf("ports = %v, want the first port after the two listeners (%d)", result.Ports, base+2)
	}
}

func TestPortsHeldByAnotherKeyAreSkipped(t *testing.T) {
	key := "myapp/main"
	base := claims.DefaultRange.Base(key)
	m, st := managerWithStore(t, nil, nil)

	expires := time.Now().Add(time.Hour)
	seedClaim(t, st, base, "other/main", expires)
	seedClaim(t, st, base+1, "other/main", expires)

	result := mustAcquire(t, m, claims.Request{Key: key, Count: 2})
	if len(result.Ports) != 2 {
		t.Fatalf("ports = %v, want two", result.Ports)
	}
	for _, port := range result.Ports {
		if port == base || port == base+1 {
			t.Fatalf("ports = %v, want none of the foreign claim's block", result.Ports)
		}
	}
}

// An omitted count asks the project default, including when only a key was
// sent. An explicit count never asks, and a project without a default gets one
// port.
func TestOmittedCountUsesTheProjectDefault(t *testing.T) {
	st := openStore(t)
	counts := &projectCounts{block: 4}
	m := claims.New(st.Claims(), claims.Options{DefaultCount: counts.countFor})

	if result := mustAcquire(t, m, claims.Request{Project: "shop", Worktree: "feature-x"}); len(result.Ports) != 4 {
		t.Errorf("omitted count: ports = %v, want 4", result.Ports)
	}
	if result := mustAcquire(t, m, claims.Request{Key: "shop/feature-y"}); len(result.Ports) != 4 {
		t.Errorf("key only: ports = %v, want 4 from the key's project", result.Ports)
	}
	if result := mustAcquire(t, m, claims.Request{Project: "shop", Worktree: "feature-z", Count: 2}); len(result.Ports) != 2 {
		t.Errorf("explicit count: ports = %v, want 2", result.Ports)
	}
	if result := mustAcquire(t, m, claims.Request{Project: "other"}); len(result.Ports) != 1 {
		t.Errorf("no default: ports = %v, want 1", result.Ports)
	}
	if joined := strings.Join(counts.asked, ","); joined != "shop,shop,other" {
		t.Errorf("DefaultCount asked for %q, want shop,shop,other", joined)
	}
}

// The manifest allows a worktree block of up to 100 ports, so the manager must
// grant 100 and refuse 101.
func TestCountCapMatchesTheManifestBlock(t *testing.T) {
	m, _ := managerWithStore(t, nil, nil)
	if result := mustAcquire(t, m, claims.Request{Key: "big/main", Count: 100}); len(result.Ports) != 100 {
		t.Fatalf("ports = %d, want 100", len(result.Ports))
	}
	if _, err := m.Acquire(claims.Request{Key: "bigger/main", Count: claims.MaxCount + 1}); err == nil {
		t.Fatal("a count above MaxCount was accepted")
	}
}

func TestReacquireRefreshesExpiryAndKeepsCreation(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	clock := now
	m, st := managerWithStore(t, nil, func() time.Time { return clock })

	first := mustAcquire(t, m, claims.Request{Key: "myapp/main", TTL: time.Hour})
	if !first.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires_at = %v, want %v", first.ExpiresAt, now.Add(time.Hour))
	}

	clock = now.Add(30 * time.Minute)
	second := mustAcquire(t, m, claims.Request{Key: "myapp/main", TTL: time.Hour})
	if second.Ports[0] != first.Ports[0] {
		t.Fatalf("refresh moved the port: %v -> %v", first.Ports, second.Ports)
	}
	if !second.ExpiresAt.Equal(clock.Add(time.Hour)) {
		t.Errorf("expires_at = %v, want the refreshed value", second.ExpiresAt)
	}

	rows := heldRows(t, st, "myapp/main")
	if len(rows) != 1 {
		t.Fatalf("held rows = %v, want one", rows)
	}
	if created := rows[0].CreatedAt; !created.Equal(now) {
		t.Errorf("created_at = %v, want the original %v", created, now)
	}
}

func TestExpiredClaimsAreSweptAndTheirPortsReused(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	clock := now
	m, st := managerWithStore(t, nil, func() time.Time { return clock })

	stale := mustAcquire(t, m, claims.Request{Key: "gone/main", TTL: time.Minute})
	clock = now.Add(2 * time.Minute)

	held, err := m.Held()
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if len(held) != 0 {
		t.Fatalf("Held = %+v, want the expired claim swept", held)
	}
	if rows := allClaims(t, st); len(rows) != 0 {
		t.Fatalf("rows = %v, want the table swept", rows)
	}

	seedClaim(t, st, stale.Ports[0], "next/main", clock.Add(time.Hour))
}

func TestCountShrinksAndGrowsAroundTheSamePorts(t *testing.T) {
	m, _ := managerWithStore(t, nil, nil)
	three := mustAcquire(t, m, claims.Request{Key: "myapp/main", Count: 3})
	if len(three.Ports) != 3 {
		t.Fatalf("ports = %v, want three", three.Ports)
	}

	one := mustAcquire(t, m, claims.Request{Key: "myapp/main", Count: 1})
	if len(one.Ports) != 1 || one.Ports[0] != three.Ports[0] {
		t.Fatalf("ports = %v, want just %d", one.Ports, three.Ports[0])
	}
	held, err := m.Held()
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if len(held) != 1 || held[three.Ports[0]] != "myapp/main" {
		t.Fatalf("held = %+v, want the surplus released", held)
	}

	grown := mustAcquire(t, m, claims.Request{Key: "myapp/main", Count: 3})
	if len(grown.Ports) != 3 || grown.Ports[0] != three.Ports[0] {
		t.Fatalf("ports = %v, want to grow back around %v", grown.Ports, three.Ports)
	}
}

func TestConcurrentAcquiresDoNotCollide(t *testing.T) {
	m, _ := managerWithStore(t, nil, nil)

	const workers = 24
	results := make([][]int, workers)
	failures := make([]error, workers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			result, err := m.Acquire(claims.Request{
				Project: "myapp", Worktree: string(rune('a' + index)), Count: 2,
			})
			results[index], failures[index] = result.Ports, err
		}(i)
	}
	close(start)
	group.Wait()

	owner := make(map[int]int, workers*2)
	for index, err := range failures {
		if err != nil {
			t.Fatalf("worker %d: %v", index, err)
		}
		if len(results[index]) != 2 {
			t.Fatalf("worker %d got %v, want two ports", index, results[index])
		}
		for _, port := range results[index] {
			if previous, duplicate := owner[port]; duplicate {
				t.Fatalf("workers %d and %d both got port %d", previous, index, port)
			}
			owner[port] = index
		}
	}
}

func TestReleaseIsIdempotentAndFreesThePorts(t *testing.T) {
	m, _ := managerWithStore(t, nil, nil)
	reserved := mustAcquire(t, m, claims.Request{Key: "myapp/main", Count: 2})

	if released, err := m.Release("myapp/main"); err != nil || released != 2 {
		t.Fatalf("Release = %d, %v; want 2, nil", released, err)
	}
	if released, err := m.Release("myapp/main"); err != nil || released != 0 {
		t.Fatalf("second Release = %d, %v; want 0, nil", released, err)
	}
	if held, err := m.Held(); err != nil || len(held) != 0 {
		t.Fatalf("Held = %v, %v; want empty", held, err)
	}

	again := mustAcquire(t, m, claims.Request{Key: "myapp/main", Count: 2})
	if again.Ports[0] != reserved.Ports[0] {
		t.Errorf("after release ports = %v, want %v", again.Ports, reserved.Ports)
	}
}

func TestAcquireNeedsAProjectOrKey(t *testing.T) {
	m, _ := managerWithStore(t, nil, nil)
	if _, err := m.Acquire(claims.Request{}); !errors.Is(err, claims.ErrNoProject) {
		t.Fatalf("err = %v, want ErrNoProject", err)
	}
}

func TestFullRangeIsReported(t *testing.T) {
	st := openStore(t)
	listening := map[int]bool{20000: true, 20001: true, 20002: true, 20003: true}
	m := claims.New(st.Claims(), claims.Options{
		Range:     claims.Range{Start: 20000, End: 20003},
		Listening: func() (map[int]bool, error) { return listening, nil },
	})
	if _, err := m.Acquire(claims.Request{Key: "myapp/main"}); !errors.Is(err, claims.ErrExhausted) {
		t.Fatalf("err = %v, want ErrExhausted", err)
	}
}

func TestAKeyKeepsItsPortWhileItsOwnListenerRuns(t *testing.T) {
	key := "myapp/main"
	base := claims.DefaultRange.Base(key)
	listening := map[int]bool{}
	m, _ := managerWithStore(t, listening, nil)

	first := mustAcquire(t, m, claims.Request{Key: key})
	listening[first.Ports[0]] = true
	second := mustAcquire(t, m, claims.Request{Key: key})
	if second.Ports[0] != base {
		t.Fatalf("ports = %v, want to keep %d", second.Ports, base)
	}
}

func TestNarrowTableVerifiesWithABindProbe(t *testing.T) {
	st := openStore(t)
	key := "myapp/feature-x"
	base := claims.DefaultRange.Base(key)
	var probed []int
	m := claims.New(st.Claims(), claims.Options{
		Listening: func() (map[int]bool, error) { return nil, nil },
		Narrow:    true,
		Bindable: func(port int) bool {
			probed = append(probed, port)
			return port != base
		},
	})
	result := mustAcquire(t, m, claims.Request{Project: "myapp", Worktree: "feature-x"})
	if result.Ports[0] == base {
		t.Fatalf("ports = %v, want the rejected %d skipped", result.Ports, base)
	}
	if len(probed) == 0 || probed[0] != base {
		t.Fatalf("probed = %v, want the derived base checked first", probed)
	}
}

func TestNarrowTableWithoutABindProbeFailsClosed(t *testing.T) {
	st := openStore(t)
	m := claims.New(st.Claims(), claims.Options{
		Listening: func() (map[int]bool, error) { return nil, nil },
		Narrow:    true,
	})
	if _, err := m.Acquire(claims.Request{Key: "myapp/main"}); !errors.Is(err, claims.ErrNarrowUnverified) {
		t.Fatalf("err = %v, want ErrNarrowUnverified", err)
	}
}
