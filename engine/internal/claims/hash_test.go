package claims

import "testing"

// The derived port is a contract between every option-berth on the machine —
// two checkouts only agree on where a service lives because they both compute
// the same number — so the hash is pinned to golden values here. A change to
// fnv1a32, to the key shape or to the window moves every service's port and
// must be a deliberate edit to this table, not a silent refactor.
func TestBaseIsPinnedAndProcessIndependent(t *testing.T) {
	// Three-segment keys, because that is what a service reserves under; a
	// two-segment key would be a checkout-wide claim this product does not
	// have. A row whose key carries the product name moves when the product is
	// renamed, because the key is data and not a label.
	golden := map[string]int{
		"option-berth/main/api": 20510,
		"myapp/feature-x/api":   21570,
		"myapp/feature-y/api":   23800,
		"option-berth/2A.6/api": 16190,
	}
	window := DefaultRange()
	for key, want := range golden {
		if got := window.Base(key); got != want {
			t.Errorf("Base(%q) = %d, want the pinned %d (the derivation moved)", key, got, want)
		}
	}
}

func TestBaseIsInsideTheWindowAndOnABlockBoundary(t *testing.T) {
	window := DefaultRange()
	for _, key := range []string{"a/b/c", "option-berth/main/api", "x/y/z", "long-project-name/some-long-worktree-name/api"} {
		got := window.Base(key)
		if got < window.First() || got > window.Last() {
			t.Errorf("Base(%q) = %d, outside %d-%d", key, got, window.First(), window.Last())
		}
		if (got-window.First())%blockSize != 0 {
			t.Errorf("Base(%q) = %d, not on a %d-port block boundary", key, got, blockSize)
		}
	}
}

func TestDifferentWorktreesLandInDifferentBlocks(t *testing.T) {
	window := DefaultRange()
	a := window.Base(Address{Project: "myapp", Worktree: "feature-x", Service: "api"}.Key())
	b := window.Base(Address{Project: "myapp", Worktree: "feature-y", Service: "api"}.Key())
	if a == b {
		t.Fatalf("two worktrees derived the same base port %d", a)
	}
}

func TestWalkCountsUpwardAndWrapsInsideTheWindow(t *testing.T) {
	window, err := RangeOf(10000, 10009)
	if err != nil {
		t.Fatalf("RangeOf: %v", err)
	}
	base := window.Base("wrap/me/api")
	seen := map[int]bool{}
	for i := 0; i < window.Span(); i++ {
		p := window.Walk(base, i)
		if p < window.First() || p > window.Last() {
			t.Fatalf("Walk(%d) = %d, outside %d-%d", i, p, window.First(), window.Last())
		}
		if seen[p] {
			t.Fatalf("Walk(%d) = %d repeated before the window was exhausted", i, p)
		}
		seen[p] = true
	}
	if len(seen) != window.Span() {
		t.Fatalf("walked %d distinct ports, want the whole %d-port window", len(seen), window.Span())
	}
	if got, want := window.Walk(base, 1), window.First()+(base-window.First()+1)%window.Span(); got != want {
		t.Errorf("Walk(1) = %d, want the port after the base (%d)", got, want)
	}
}

// An address key is `<project>/<worktree>/<service>`: the checkout and the
// service together, so two services of one checkout are two rows.
func TestTheAddressKeyNamesTheCheckoutAndTheService(t *testing.T) {
	cases := []struct {
		address Address
		key     string
	}{
		{Address{Project: "option-berth", Service: "api"}, "option-berth/" + DefaultWorktree + "/api"},
		{Address{Project: "option-berth", Worktree: "main", Service: "api"}, "option-berth/main/api"},
		{Address{Project: " myapp ", Worktree: " feature-x ", Service: " api "}, "myapp/feature-x/api"},
		{Address{Project: "/myapp/", Worktree: "/feature-x/", Service: "api"}, "myapp/feature-x/api"},
	}
	for _, c := range cases {
		if got := c.address.Key(); got != c.key {
			t.Errorf("%+v.Key() = %q, want %q", c.address, got, c.key)
		}
	}
}

// A window no port could be derived from is refused at construction, so a
// running book never holds one.
func TestARangeThatCouldNotHoldAPortIsRefused(t *testing.T) {
	if _, err := RangeOf(10000, 32699); err != nil {
		t.Fatalf("RangeOf(refused a real window) = %v", err)
	}
	for _, bounds := range [][2]int{{0, 100}, {100, 70000}, {900, 800}} {
		if _, err := RangeOf(bounds[0], bounds[1]); err == nil {
			t.Errorf("%d-%d was accepted", bounds[0], bounds[1])
		}
	}
	// The zero value is the product's own window, not an empty one.
	if DefaultRange().Span() != 22700 {
		t.Errorf("the default window holds %d ports, want 22700", DefaultRange().Span())
	}
}
