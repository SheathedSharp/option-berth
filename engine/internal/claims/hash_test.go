package claims

import "testing"

// The derived port is shared contract: every option-berth on the machine
// computes it independently, so a change to the hash, the key shape or the
// default range moves every worktree's ports. The numbers below are frozen on
// purpose; changing one is a deliberate contract change, not a refactor.
func TestDerivedPortsAreFrozen(t *testing.T) {
	pinned := []struct {
		key  string
		port int
	}{
		{"option-berth/main", 15680},
		{"myapp/feature-x", 21700},
		{"myapp/feature-y", 22190},
		{"option-berth/2A.6-claims", 21620},
		{"", 11110},
	}
	for _, tc := range pinned {
		if got := DefaultRange.Base(tc.key); got != tc.port {
			t.Errorf("Base(%q) = %d, want the frozen %d", tc.key, got, tc.port)
		}
	}
}

func TestBaseStaysInsideTheWindowOnABlockBoundary(t *testing.T) {
	keys := []string{"a/b", "option-berth/main", "p/q", "long-project-name/long-worktree-name"}
	for _, key := range keys {
		base := DefaultRange.Base(key)
		if base < DefaultRange.Start || base > DefaultRange.End {
			t.Errorf("Base(%q) = %d, outside %d-%d", key, base, DefaultRange.Start, DefaultRange.End)
		}
		if (base-DefaultRange.Start)%BlockSize != 0 {
			t.Errorf("Base(%q) = %d, not aligned to a %d-port block", key, base, BlockSize)
		}
	}
}

func TestSiblingWorktreesDoNotShareABlock(t *testing.T) {
	x := DefaultRange.Base(Key("myapp", "feature-x"))
	y := DefaultRange.Base(Key("myapp", "feature-y"))
	if x == y {
		t.Fatalf("two worktrees derived the same base %d", x)
	}
}

func TestAtCoversTheWindowOnceAndWraps(t *testing.T) {
	window := Range{Start: 10000, End: 10009}
	key := "wrap/me"
	base := window.Base(key)

	seen := make(map[int]bool, window.Span())
	for offset := 0; offset < window.Span(); offset++ {
		port := window.At(key, offset)
		if port < window.Start || port > window.End {
			t.Fatalf("At(%d) = %d, outside the window", offset, port)
		}
		if seen[port] {
			t.Fatalf("At(%d) repeated port %d before the window was covered", offset, port)
		}
		seen[port] = true
	}
	if len(seen) != window.Span() {
		t.Fatalf("covered %d ports, want %d", len(seen), window.Span())
	}
	if next := window.At(key, 1); next != window.Start+(base-window.Start+1)%window.Span() {
		t.Errorf("At(1) = %d, want the port after the base", next)
	}
}

func TestKeyDefaultsAndTrimming(t *testing.T) {
	cases := []struct {
		project  string
		worktree string
		want     string
	}{
		{"option-berth", "", "option-berth/" + DefaultWorktree},
		{"option-berth", "main", "option-berth/main"},
		{" myapp ", " feature-x ", "myapp/feature-x"},
		{"/myapp/", "/feature-x/", "myapp/feature-x"},
	}
	for _, tc := range cases {
		if got := Key(tc.project, tc.worktree); got != tc.want {
			t.Errorf("Key(%q, %q) = %q, want %q", tc.project, tc.worktree, got, tc.want)
		}
	}
}

func TestSplitKeyRecoversItsParts(t *testing.T) {
	project, worktree := SplitKey("myapp/feature-x")
	if project != "myapp" || worktree != "feature-x" {
		t.Errorf("SplitKey = %q/%q", project, worktree)
	}
	project, worktree = SplitKey("myapp")
	if project != "myapp" || worktree != DefaultWorktree {
		t.Errorf("SplitKey of a bare project = %q/%q", project, worktree)
	}
}

func TestRangeValidation(t *testing.T) {
	if err := DefaultRange.Valid(); err != nil {
		t.Fatalf("DefaultRange is invalid: %v", err)
	}
	invalid := []Range{{0, 100}, {100, 70000}, {900, 800}}
	for _, window := range invalid {
		if err := window.Valid(); err == nil {
			t.Errorf("%v was accepted", window)
		}
	}
}
