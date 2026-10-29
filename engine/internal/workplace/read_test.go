package workplace

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestTargetCreatesTheDefaultWorkplaceOnFirstUse(t *testing.T) {
	s := testStore(t)
	root := "/home/dev/code/demo"

	got, err := Target(s, root)
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	if got.Name != DefaultName || !got.Active {
		t.Fatalf("first Target = %+v, want an active %q", got, DefaultName)
	}
	// Asking again returns the same one rather than a second.
	again, err := Target(s, root)
	if err != nil {
		t.Fatalf("Target again: %v", err)
	}
	if again.ID != got.ID {
		t.Fatalf("Target created a second workplace: %q then %q", got.ID, again.ID)
	}
	// And it is per worktree.
	other, err := Target(s, "/home/dev/code/other")
	if err != nil {
		t.Fatalf("Target other: %v", err)
	}
	if other.ID == got.ID {
		t.Fatal("two worktrees share one workplace")
	}
}

func TestTargetFallsBackToTheNewestWhenNoneIsActive(t *testing.T) {
	s := testStore(t)
	root := "/home/dev/code/demo"
	// A row written without the active flag: the state a hand-edited or
	// partially migrated database is in.
	if err := s.Workplaces().Create(store.WorkplaceRow{ID: "only", Worktree: root, Name: "kept"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := Target(s, root)
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	if got.ID != "only" || !got.Active {
		t.Fatalf("Target = %+v, want the existing one made active", got)
	}
	if _, err := Target(s, root); err != nil {
		t.Fatalf("Target after activating: %v", err)
	}
	rows, err := List(s, root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Target created a workplace beside the existing one: %+v", rows)
	}
}

func TestListResultMarksTheActiveWorkplace(t *testing.T) {
	s := testStore(t)
	root := "/home/dev/code/demo"
	if _, err := Create(s, root, "default"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := Create(s, root, "spike")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Workplaces().AppendBlock(store.BlockRow{
		ID: "b1", Workplace: second.ID, Kind: "shell", Source: "engine", Status: "completed",
	}); err != nil {
		t.Fatalf("AppendBlock: %v", err)
	}

	res, err := ListResult(s, root)
	if err != nil {
		t.Fatalf("ListResult: %v", err)
	}
	if res.Worktree != root || res.Active != second.ID {
		t.Fatalf("ListResult = %+v", res)
	}
	if len(res.Workplaces) != 2 {
		t.Fatalf("ListResult returned %d workplaces", len(res.Workplaces))
	}
	// Oldest first, and the newest one carries the block count.
	if res.Workplaces[0].Name != "default" || res.Workplaces[0].Active {
		t.Fatalf("first row = %+v", res.Workplaces[0])
	}
	if res.Workplaces[1].Blocks != 1 || !res.Workplaces[1].Active {
		t.Fatalf("second row = %+v", res.Workplaces[1])
	}

	empty, err := ListResult(s, "/home/dev/code/nothing")
	if err != nil {
		t.Fatalf("ListResult empty: %v", err)
	}
	if len(empty.Workplaces) != 0 || empty.Active != "" {
		t.Fatalf("a worktree with no workplace = %+v", empty)
	}
}

func TestShowResultReadsTheRecordAndSaysWhenItIsCapped(t *testing.T) {
	s := testStore(t)
	root := "/home/dev/code/demo"
	wp, err := Create(s, root, "default")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, id := range []string{"b1", "b2", "b3"} {
		if _, err := s.Workplaces().AppendBlock(store.BlockRow{
			ID: id, Workplace: wp.ID, Kind: "shell", Source: "engine",
			Input: "cmd " + id, Status: "completed",
		}); err != nil {
			t.Fatalf("AppendBlock %s: %v", id, err)
		}
	}

	// No selector means the active workplace.
	res, err := ShowResult(s, root, "", 0)
	if err != nil {
		t.Fatalf("ShowResult: %v", err)
	}
	if res.Workplace.ID != wp.ID || len(res.Blocks) != 3 || res.Truncated {
		t.Fatalf("ShowResult = %+v", res)
	}
	if res.Blocks[0].Input != "cmd b1" || res.Blocks[2].Input != "cmd b3" {
		t.Fatalf("blocks are not oldest first: %+v", res.Blocks)
	}

	// A limit keeps the newest, and the flag says the list is not the whole
	// record.
	capped, err := ShowResult(s, root, "default", 2)
	if err != nil {
		t.Fatalf("ShowResult capped: %v", err)
	}
	if len(capped.Blocks) != 2 || !capped.Truncated {
		t.Fatalf("capped ShowResult = %+v", capped)
	}
	if capped.Blocks[0].Input != "cmd b2" || capped.Blocks[1].Input != "cmd b3" {
		t.Fatalf("the cap kept the wrong end: %+v", capped.Blocks)
	}

	if _, err := ShowResult(s, root, "missing", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ShowResult(unknown) = %v, want ErrNotFound", err)
	}
	if _, err := ShowResult(s, root, "", -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ShowResult(negative limit) = %v, want ErrInvalid", err)
	}
}

func TestShowResultReadsWithoutCreatingAnything(t *testing.T) {
	s := testStore(t)
	root := "/home/dev/code/demo"

	// A worktree nobody has worked in has nothing to show, and looking must not
	// bring a workplace into being: recording is what creates one.
	if _, err := ShowResult(s, root, "", 0); !errors.Is(err, ErrNoWorkplace) {
		t.Fatalf("ShowResult on an empty worktree = %v, want ErrNoWorkplace", err)
	}
	if rows, err := List(s, root); err != nil || len(rows) != 0 {
		t.Fatalf("reading created a workplace: %+v (%v)", rows, err)
	}

	// The same for a worktree whose workplaces carry no active flag: reading
	// shows the newest without moving where new work lands.
	if err := s.Workplaces().Create(store.WorkplaceRow{ID: "old", Worktree: root, Name: "old"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Workplaces().Create(store.WorkplaceRow{ID: "new", Worktree: root, Name: "new"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := ShowResult(s, root, "", 0)
	if err != nil {
		t.Fatalf("ShowResult: %v", err)
	}
	if res.Workplace.ID != "new" {
		t.Fatalf("ShowResult picked %q, want the newest", res.Workplace.ID)
	}
	if res.Workplace.Active {
		t.Fatal("reading reported the newest as active")
	}
	rows, err := List(s, root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, w := range rows {
		if w.Active {
			t.Fatalf("reading activated %q", w.ID)
		}
	}
}

func TestFindSaysWhenThereIsNoWorkplaceYet(t *testing.T) {
	s := testStore(t)
	if _, _, err := Find(s, "/nowhere", "default"); !errors.Is(err, ErrNoWorkplace) {
		t.Fatalf("Find on an empty worktree = %v, want ErrNoWorkplace", err)
	}
}

func TestStatusForRunExit(t *testing.T) {
	cases := []struct {
		code   int
		reason string
		want   Status
	}{
		{0, runsreg.ReasonExited, StatusCompleted},
		{1, runsreg.ReasonCrashed, StatusFailed},
		{0, runsreg.ReasonStopped, StatusInterrupted},
		// A stopped run is interrupted whatever code the signal left: reading
		// the number instead of the reason would call a deliberate stop a
		// failure.
		{143, runsreg.ReasonStopped, StatusInterrupted},
		{1, runsreg.ReasonStartFailed, StatusFailed},
		{1, runsreg.ReasonReadyTimeout, StatusFailed},
		{1, runsreg.ReasonDependencyNotReady, StatusFailed},
		{1, runsreg.ReasonPortOccupied, StatusFailed},
	}
	for _, c := range cases {
		if got := StatusForRunExit(c.code, c.reason); got != c.want {
			t.Errorf("StatusForRunExit(%d, %q) = %q, want %q", c.code, c.reason, got, c.want)
		}
	}
}
