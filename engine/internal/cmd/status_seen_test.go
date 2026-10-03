package cmd

import (
	"os"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/paths"
)

func intPtr(v int) *int { return &v }

func seenFixture() (before, now statusSeenEntry) {
	before = statusSeenEntry{
		At:   "2026-09-24T04:00:00Z",
		Head: "d0a34f83edf1c5a535f7591d85173286b8125d1e",
		Files: []statusSeenFile{
			{Path: "a.go", Status: " M", Additions: intPtr(3), Deletions: intPtr(1)},
			{Path: "b.go", Status: "??"},
			{Path: "gone.go", Status: " M"},
		},
		Services: []statusSeenSvc{
			{Name: "api", Running: true, Port: 18090},
			{Name: "worker", Running: false},
			{Name: "web", Running: true, Port: 5173},
		},
		Ports: []int{5173, 18090},
	}
	now = statusSeenEntry{
		At:   "2026-09-24T05:00:00Z",
		Head: "9f3c1a2b4d5e6f708192a3b4c5d6e7f8091a2b3c",
		Files: []statusSeenFile{
			{Path: "a.go", Status: " M", Additions: intPtr(9), Deletions: intPtr(1)}, // touched: counts moved
			{Path: "c.go", Status: "??"}, // appeared
		},
		Services: []statusSeenSvc{
			{Name: "api", Running: false}, // stopped
			{Name: "worker", Running: true, Port: 19000},
			{Name: "web", Running: true, Port: 5173},
		},
		Ports: []int{5173, 19000},
	}
	return before, now
}

// TestCompareStatusSeenNamesWhatMoved is J6's whole contract: what appeared,
// what is gone, what moved — and nothing about whether any of it is good.
func TestCompareStatusSeenNamesWhatMoved(t *testing.T) {
	before, now := seenFixture()
	got := compareStatusSeen(before, now)

	if got.Empty {
		t.Fatal("a run with three kinds of change reports itself empty")
	}
	if len(got.ServicesStarted) != 1 || got.ServicesStarted[0] != "worker" {
		t.Errorf("services_started = %v, want worker", got.ServicesStarted)
	}
	if len(got.ServicesStopped) != 1 || got.ServicesStopped[0] != "api" {
		t.Errorf("services_stopped = %v, want api", got.ServicesStopped)
	}
	if len(got.PortsOpened) != 1 || got.PortsOpened[0] != 19000 {
		t.Errorf("ports_opened = %v, want 19000", got.PortsOpened)
	}
	if len(got.PortsClosed) != 1 || got.PortsClosed[0] != 18090 {
		t.Errorf("ports_closed = %v, want 18090", got.PortsClosed)
	}
	if len(got.Files.Appeared) != 1 || got.Files.Appeared[0] != "c.go" {
		t.Errorf("files.appeared = %v, want c.go", got.Files.Appeared)
	}
	if len(got.Files.Touched) != 1 || got.Files.Touched[0] != "a.go" {
		t.Errorf("files.touched = %v, want a.go (its counts moved)", got.Files.Touched)
	}
	if len(got.Files.Gone) != 2 || got.Files.Gone[0] != "b.go" {
		t.Errorf("files.gone = %v, want b.go and gone.go", got.Files.Gone)
	}
	if got.HeadFrom != "d0a34f8" || got.HeadTo != "9f3c1a2" {
		t.Errorf("head = %s → %s, want the two short hashes", got.HeadFrom, got.HeadTo)
	}
	if got.At != before.At {
		t.Errorf("changed.at = %q, want the mark's own time %q", got.At, before.At)
	}
}

// TestCompareStatusSeenIsEmptyWhenNothingMoved: "nothing changed" has to be a
// real answer and not a missing one.
func TestCompareStatusSeenIsEmptyWhenNothingMoved(t *testing.T) {
	_, now := seenFixture()
	got := compareStatusSeen(now, now)
	if !got.Empty {
		t.Errorf("an unchanged state reports %+v", got)
	}
}

// TestStatusSeenMarkDoesNotMoveWhileNothingChanges: the mark's time is when
// this state was *first* seen, which is what makes "nothing changed since"
// true.
func TestStatusSeenMarkDoesNotMoveWhileNothingChanges(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())

	doc := loadStatusSeen()
	entry := statusSeenEntry{At: "2026-09-24T04:00:00Z", Head: "abc", Ports: []int{3000}}

	if _, had := doc.mark("/repo", entry); had {
		t.Fatal("the first mark claims there was one before it")
	}

	// A second look at the same state keeps the original mark.
	later := entry
	later.At = "2026-09-24T05:00:00Z"
	before, had := doc.mark("/repo", later)
	if !had {
		t.Fatal("the second mark lost the first one")
	}
	if before.At != entry.At {
		t.Errorf("before.At = %q, want the original %q", before.At, entry.At)
	}
	reloaded := loadStatusSeen()
	if got := reloaded.Roots["/repo"].At; got != entry.At {
		t.Errorf("the stored mark moved to %q, want it to stay at %q", got, entry.At)
	}

	// A real change does move it.
	later.Ports = []int{3000, 3001}
	if _, had := doc.mark("/repo", later); !had {
		t.Fatal("the third mark lost the second one")
	}
	if got := loadStatusSeen().Roots["/repo"].At; got != later.At {
		t.Errorf("stored mark = %q, want the new %q", got, later.At)
	}
}

// TestStatusSeenUnreadableReadsAsEmpty: the ledger is context, and a broken one
// must never be the reason a command fails.
func TestStatusSeenUnreadableReadsAsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)

	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SeenCLI(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := loadStatusSeen()
	if len(doc.Roots) != 0 {
		t.Errorf("a broken ledger produced %d entries", len(doc.Roots))
	}
}

// TestStatusSeenPrunesWhatNobodyLookedAt: a month-old mark is noise, and the
// prune happens on write so the file cannot grow forever.
func TestStatusSeenPrunesWhatNobodyLookedAt(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())

	old := time.Now().Add(-maxSeenAge - time.Hour).UTC().Format(time.RFC3339)
	doc := loadStatusSeen()
	doc.Roots["/stale"] = statusSeenEntry{At: old}
	doc.Roots["/fresh"] = statusSeenEntry{At: time.Now().UTC().Format(time.RFC3339)}
	if err := doc.save(); err != nil {
		t.Fatal(err)
	}

	again := loadStatusSeen()
	again.mark("/new", statusSeenEntry{At: time.Now().UTC().Format(time.RFC3339)})
	after := loadStatusSeen()
	if _, ok := after.Roots["/stale"]; ok {
		t.Error("a mark nothing had looked at in a month survived a write")
	}
	for _, keep := range []string{"/fresh", "/new"} {
		if _, ok := after.Roots[keep]; !ok {
			t.Errorf("%s was pruned along with it", keep)
		}
	}
}
