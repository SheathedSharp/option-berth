package claims_test

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/claims"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// newBook builds a book over a real temp database, so the SQL that enforces one
// holder per port is exercised together with the probe.
func newBook(t *testing.T, serving map[int]bool, clock func() time.Time) (*claims.Book, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return claims.New(st.Claims(), claims.Options{
		Clock:     clock,
		Listening: func() (map[int]bool, error) { return serving, nil },
	}), st
}

func addr(project, worktree, service string) claims.Address {
	return claims.Address{Project: project, Worktree: worktree, Service: service}
}

func reserve(t *testing.T, b *claims.Book, a claims.Address) claims.Reservation {
	t.Helper()
	got, err := b.Reserve(a)
	if err != nil {
		t.Fatalf("Reserve(%+v): %v", a, err)
	}
	return got
}

// A service keeps its address: it takes the port derived from its own key, and
// a second start hands back that same port rather than probing again.
func TestAServiceKeepsItsAddress(t *testing.T) {
	b, _ := newBook(t, nil, nil)
	key := addr("myapp", "feature-x", "api").Key()
	want := claims.DefaultRange().Base(key)

	first := reserve(t, b, addr("myapp", "feature-x", "api"))
	if first.Port != want {
		t.Fatalf("port = %d, want the derived %d", first.Port, want)
	}
	if first.Key() != key {
		t.Errorf("key = %q, want %q", first.Key(), key)
	}
	second := reserve(t, b, addr("myapp", "feature-x", "api"))
	if second.Port != first.Port {
		t.Errorf("second reserve = %d, want the same %d", second.Port, first.Port)
	}
}

// Two checkouts of one repository never share an address, which is the whole
// reason a reservation exists.
func TestSiblingCheckoutsNeverShareAnAddress(t *testing.T) {
	b, _ := newBook(t, nil, nil)
	a := reserve(t, b, addr("myapp", "feature-x", "api"))
	c := reserve(t, b, addr("myapp", "feature-y", "api"))
	if a.Port == c.Port {
		t.Fatalf("both checkouts got port %d", a.Port)
	}
}

// Two services in one checkout are two addresses, not one: each `port: auto`
// service gets its own port.
func TestTwoServicesInOneCheckoutTakeTwoAddresses(t *testing.T) {
	b, _ := newBook(t, nil, nil)
	api := reserve(t, b, addr("myapp", "main", "api"))
	web := reserve(t, b, addr("myapp", "main", "web"))
	if api.Port == web.Port {
		t.Fatalf("api and web both got port %d", api.Port)
	}
}

func TestAPortSomethingIsServingIsSkipped(t *testing.T) {
	key := addr("myapp", "main", "api").Key()
	base := claims.DefaultRange().Base(key)
	b, _ := newBook(t, map[int]bool{base: true, base + 1: true}, nil)

	got := reserve(t, b, addr("myapp", "main", "api"))
	if got.Port != base+2 {
		t.Fatalf("port = %d, want the first port past the two listeners (%d)", got.Port, base+2)
	}
}

func TestAPortAnotherServiceHoldsIsSkipped(t *testing.T) {
	key := addr("myapp", "main", "api").Key()
	base := claims.DefaultRange().Base(key)
	b, st := newBook(t, nil, nil)

	at := time.Now()
	if err := st.Claims().Put(
		store.ClaimRow{Port: base, Key: "other/main/api", Project: "other", Worktree: "main", Since: at, Until: at.Add(time.Hour)},
		store.ClaimRow{Port: base + 1, Key: "other/main/api", Project: "other", Worktree: "main", Since: at, Until: at.Add(time.Hour)},
	); err != nil {
		t.Fatalf("seeding a foreign reservation: %v", err)
	}

	got := reserve(t, b, addr("myapp", "main", "api"))
	if got.Port == base || got.Port == base+1 {
		t.Fatalf("port = %d, want neither of the foreign reservation's ports", got.Port)
	}
}

// A service that is already listening on its own reserved port asks again: the
// answer is that same port, not the next free one. This is what makes a second
// `up`, or a `restart --only`, keep the address the service is already using.
func TestThePortAServiceHoldsIsKeptEvenWhileItListens(t *testing.T) {
	key := addr("myapp", "main", "api").Key()
	base := claims.DefaultRange().Base(key)
	serving := map[int]bool{}
	b, _ := newBook(t, serving, nil)

	first := reserve(t, b, addr("myapp", "main", "api"))
	serving[first.Port] = true
	second := reserve(t, b, addr("myapp", "main", "api"))
	if second.Port != base {
		t.Fatalf("port = %d, want to keep %d", second.Port, base)
	}
}

func TestReservingAgainRenewsTheLeaseAndKeepsTheStart(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	clock := now
	b, st := newBook(t, nil, func() time.Time { return clock })

	first := reserve(t, b, addr("myapp", "main", "api"))
	if !first.Until.Equal(now.Add(claims.DefaultLease)) {
		t.Fatalf("until = %v, want %v", first.Until, now.Add(claims.DefaultLease))
	}
	if !first.Since.Equal(now) {
		t.Fatalf("since = %v, want %v", first.Since, now)
	}

	clock = now.Add(30 * time.Minute)
	second := reserve(t, b, addr("myapp", "main", "api"))
	if second.Port != first.Port {
		t.Fatalf("renewal moved the port: %d -> %d", first.Port, second.Port)
	}
	if !second.Until.Equal(clock.Add(claims.DefaultLease)) {
		t.Errorf("until = %v, want the renewed %v", second.Until, clock.Add(claims.DefaultLease))
	}
	if !second.Since.Equal(now) {
		t.Errorf("since = %v, want the original %v", second.Since, now)
	}

	rows, err := st.Claims().Get(addr("myapp", "main", "api").Key())
	if err != nil || len(rows) != 1 {
		t.Fatalf("Get = %v, %v", rows, err)
	}
	if !rows[0].Since.Equal(now) {
		t.Errorf("created_at = %v, want the original %v", rows[0].Since, now)
	}
	if !rows[0].Until.Equal(clock.Add(claims.DefaultLease)) {
		t.Errorf("expires_at = %v, want the renewed one", rows[0].Until)
	}
}

// A reservation nobody renewed lapses, and its port is offered again: that is
// how a checkout somebody abandoned stops holding an address.
func TestALapsedReservationLetsItsPortGo(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	clock := now
	b, st := newBook(t, nil, func() time.Time { return clock })

	stale := reserve(t, b, addr("gone", "main", "api"))

	clock = now.Add(claims.DefaultLease + time.Minute)
	if _, err := b.Reserve(addr("next", "main", "api")); err != nil {
		t.Fatalf("reserving after the lapse: %v", err)
	}
	rows, err := st.Claims().Get(addr("gone", "main", "api").Key())
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows for the lapsed address = %v, %v; want the reservation swept", rows, err)
	}
	if err := st.Claims().Put(store.ClaimRow{
		Port: stale.Port, Key: "reclaimed/main/api", Project: "reclaimed", Worktree: "main",
		Since: clock, Until: clock.Add(time.Hour),
	}); err != nil {
		t.Fatalf("reclaiming the lapsed port: %v", err)
	}
}

func TestConcurrentReservesNeverCollide(t *testing.T) {
	b, _ := newBook(t, nil, nil)

	// 24 checkouts start at once. Each must come back with a port no other
	// worker was given: the book serialises reserve-and-write, and a failure
	// here means two services would run on one address.
	const workers = 24
	type outcome struct {
		worktree string
		port     int
		err      error
	}
	results := make(chan outcome, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worktree string) {
			defer wg.Done()
			<-start
			got, err := b.Reserve(addr("myapp", worktree, "api"))
			results <- outcome{worktree: worktree, port: got.Port, err: err}
		}(string(rune('a' + i)))
	}
	close(start)
	wg.Wait()
	close(results)

	holder := map[int]string{}
	for got := range results {
		if got.err != nil {
			t.Fatalf("checkout %s: %v", got.worktree, got.err)
		}
		if prev, dup := holder[got.port]; dup {
			t.Fatalf("checkouts %s and %s both got port %d", prev, got.worktree, got.port)
		}
		holder[got.port] = got.worktree
	}
	if len(holder) != workers {
		t.Fatalf("%d checkouts hold %d ports, want one each", workers, len(holder))
	}
}

func TestAFullWindowIsReportedNotSilentlyShort(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	full := map[int]bool{20000: true, 20001: true, 20002: true, 20003: true}
	window, err := claims.RangeOf(20000, 20003)
	if err != nil {
		t.Fatalf("RangeOf: %v", err)
	}
	b := claims.New(st.Claims(), claims.Options{
		Range:     window,
		Listening: func() (map[int]bool, error) { return full, nil },
	})
	if _, err := b.Reserve(addr("myapp", "main", "api")); !errors.Is(err, claims.ErrExhausted) {
		t.Fatalf("err = %v, want ErrExhausted", err)
	}
}

// A narrow listener table cannot say that a port is free; a real bind can. The
// rejected candidate is skipped, not handed to a service that would fail on it.
func TestANarrowTableVerifiesEachCandidateByBinding(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	key := addr("myapp", "feature-x", "api").Key()
	base := claims.DefaultRange().Base(key)
	probed := []int{}
	b := claims.New(st.Claims(), claims.Options{
		Listening: func() (map[int]bool, error) { return nil, nil },
		Narrow:    true,
		Bindable: func(port int) bool {
			probed = append(probed, port)
			return port != base
		},
	})
	got := reserve(t, b, addr("myapp", "feature-x", "api"))
	if got.Port == base {
		t.Fatalf("port = %d, want the rejected %d skipped", got.Port, base)
	}
	if len(probed) == 0 || probed[0] != base {
		t.Fatalf("probed = %v, want the derived port checked first", probed)
	}
}

// Narrow without a bind probe would be a guess. Refusing is the only safe
// answer; the caller can still declare a fixed port.
func TestANarrowTableWithoutABindProbeRefuses(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	b := claims.New(st.Claims(), claims.Options{
		Listening: func() (map[int]bool, error) { return nil, nil },
		Narrow:    true,
	})
	if _, err := b.Reserve(addr("myapp", "main", "api")); !errors.Is(err, claims.ErrNarrowUnverified) {
		t.Fatalf("err = %v, want ErrNarrowUnverified", err)
	}
}

// An address that names no checkout or no service is refused rather than
// stored: `down --worktree` looks reservations up by the checkout they were
// stored under, so a row without one could never be released.
func TestAnAddressMustNameItsCheckoutAndService(t *testing.T) {
	b, _ := newBook(t, nil, nil)
	for _, a := range []claims.Address{
		{Worktree: "main", Service: "api"},
		{Project: "myapp", Worktree: "main"},
	} {
		if _, err := b.Reserve(a); !errors.Is(err, claims.ErrNoAddress) {
			t.Errorf("Reserve(%+v) = %v, want ErrNoAddress", a, err)
		}
	}
}

// The reservation carries the checkout it belongs to, so `down --worktree` can
// still find and release it after the manifest is gone.
func TestTheReservationCarriesItsCheckout(t *testing.T) {
	b, st := newBook(t, nil, nil)
	reserve(t, b, addr("myapp", "feature-x", "web"))

	rows, err := st.Claims().List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("List = %+v, %v", rows, err)
	}
	if rows[0].Project != "myapp" || rows[0].Worktree != "feature-x" {
		t.Fatalf("row names %q/%q, want myapp/feature-x", rows[0].Project, rows[0].Worktree)
	}
}

// An address whose worktree was left blank is the main checkout, not a
// nameless checkout, so the derived key and the stored row agree.
func TestABlankWorktreeIsTheMainCheckout(t *testing.T) {
	b, st := newBook(t, nil, nil)
	reserve(t, b, claims.Address{Project: "myapp", Service: "api"})

	rows, err := st.Claims().List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("List = %+v, %v", rows, err)
	}
	if rows[0].Key != "myapp/"+claims.DefaultWorktree+"/api" {
		t.Fatalf("key = %q, want the main checkout", rows[0].Key)
	}
}
