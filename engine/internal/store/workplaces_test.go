package store

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// blockOutcomes is the outcome vocabulary these tests pass in. It belongs to
// the caller in production — internal/workplace defines it and the store keeps
// no copy — so a test has to hand one over too.
var blockOutcomes = []string{"completed", "failed", "interrupted"}

func TestWorkplaceActiveIsOnePerWorktree(t *testing.T) {
	s := openTemp(t)
	w := s.Workplaces()
	root := "/home/dev/code/demo"

	if err := w.Create(WorkplaceRow{ID: "a", Worktree: root, Name: "default", Active: true}); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	// Creating a second active workplace must clear the first: the partial
	// unique index makes "one active per worktree" a property of the table.
	if err := w.Create(WorkplaceRow{ID: "b", Worktree: root, Name: "spike", Active: true}); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	rows, err := w.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(rows))
	}
	if rows[0].Active || !rows[1].Active {
		t.Fatalf("active flags = %v/%v, want only the newer one active", rows[0].Active, rows[1].Active)
	}

	// A third worktree's workplace is unaffected by any of this.
	if err := w.Create(WorkplaceRow{ID: "c", Worktree: "/other", Name: "default", Active: true}); err != nil {
		t.Fatalf("Create c: %v", err)
	}
	if err := w.SetActive(root, "a"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	rows, err = w.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !rows[0].Active || rows[1].Active {
		t.Fatalf("after SetActive active flags = %v/%v, want the first one", rows[0].Active, rows[1].Active)
	}
	other, err := w.List("/other")
	if err != nil {
		t.Fatalf("List other: %v", err)
	}
	if len(other) != 1 || !other[0].Active {
		t.Fatalf("the other worktree's active flag moved: %+v", other)
	}
}

func TestWorkplaceSetActiveRefusesAnotherWorktree(t *testing.T) {
	s := openTemp(t)
	w := s.Workplaces()
	if err := w.Create(WorkplaceRow{ID: "a", Worktree: "/one", Name: "default", Active: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := w.SetActive("/two", "a"); !errors.Is(err, ErrWorkplaceElsewhere) {
		t.Fatalf("SetActive across worktrees = %v, want ErrWorkplaceElsewhere", err)
	}
	if err := w.SetActive("/one", "missing"); !errors.Is(err, ErrWorkplaceNotFound) {
		t.Fatalf("SetActive unknown id = %v, want ErrWorkplaceNotFound", err)
	}
}

func TestWorkplaceDeleteRemovesBlocksAndReactivatesNewest(t *testing.T) {
	s := openTemp(t)
	w := s.Workplaces()
	root := "/home/dev/code/demo"
	base := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	for i, row := range []WorkplaceRow{
		{ID: "old", Worktree: root, Name: "old", CreatedAt: base},
		{ID: "new", Worktree: root, Name: "new", CreatedAt: base.Add(time.Hour), Active: true},
	} {
		if err := w.Create(row); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	if _, err := w.AppendBlock(BlockRow{ID: "b1", Workplace: "new", Kind: "shell", Source: "engine", Status: "completed", StartedAt: base}); err != nil {
		t.Fatalf("AppendBlock: %v", err)
	}

	gone, next, err := w.Delete("new")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !gone || next != "old" {
		t.Fatalf("Delete = (%v, %q), want (true, \"old\")", gone, next)
	}
	blocks, err := w.Blocks("new", 0)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) != 0 {
		t.Fatalf("the deleted workplace still has %d blocks", len(blocks))
	}
	rows, err := w.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || !rows[0].Active {
		t.Fatalf("the remaining workplace is not active: %+v", rows)
	}

	// Deleting the last one leaves the worktree with none, and says so.
	gone, next, err = w.Delete("old")
	if err != nil {
		t.Fatalf("Delete last: %v", err)
	}
	if !gone || next != "" {
		t.Fatalf("Delete last = (%v, %q), want (true, \"\")", gone, next)
	}
	// Deleting something already gone is not an error: the caller asked for it
	// to not be there.
	gone, _, err = w.Delete("old")
	if err != nil || gone {
		t.Fatalf("Delete twice = (%v, %v), want (false, nil)", gone, err)
	}
}

func TestWorkplaceBlocksKeepOrderAndTrimTheOldest(t *testing.T) {
	s := openTemp(t)
	w := s.Workplaces()
	if err := w.Create(WorkplaceRow{ID: "wp", Worktree: "/w", Name: "default", Active: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < WorkplaceBlockLimit+3; i++ {
		row, err := w.AppendBlock(BlockRow{
			ID: "b" + strconv.Itoa(i), Workplace: "wp", Kind: "shell", Source: "engine",
			Status: "completed", StartedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("AppendBlock %d: %v", i, err)
		}
		if row.Seq != int64(i+1) {
			t.Fatalf("block %d got seq %d, want %d", i, row.Seq, i+1)
		}
	}
	blocks, err := w.Blocks("wp", 0)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) != WorkplaceBlockLimit {
		t.Fatalf("kept %d blocks, want the cap of %d", len(blocks), WorkplaceBlockLimit)
	}
	if blocks[0].Seq != 4 {
		t.Fatalf("oldest kept block has seq %d, want 4 (the first three trimmed)", blocks[0].Seq)
	}
	if blocks[len(blocks)-1].ID != "b"+strconv.Itoa(WorkplaceBlockLimit+2) {
		t.Fatalf("newest block = %q", blocks[len(blocks)-1].ID)
	}

	// A limit keeps the newest, oldest first, and the count is what tells a
	// reader it was capped.
	tail, err := w.Blocks("wp", 2)
	if err != nil {
		t.Fatalf("Blocks limit: %v", err)
	}
	if len(tail) != 2 || tail[0].Seq != blocks[len(blocks)-2].Seq || tail[1].Seq != blocks[len(blocks)-1].Seq {
		t.Fatalf("Blocks(limit 2) = %+v, want the two newest", tail)
	}
	n, err := w.BlockCount("wp")
	if err != nil || n != WorkplaceBlockLimit {
		t.Fatalf("BlockCount = (%d, %v), want (%d, nil)", n, err, WorkplaceBlockLimit)
	}
}

func TestWorkplaceBlockUpsertNeverMovesBackwards(t *testing.T) {
	s := openTemp(t)
	w := s.Workplaces()
	if err := w.Create(WorkplaceRow{ID: "wp", Worktree: "/w", Name: "default", Active: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	started := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	code := 0
	// The outcome vocabulary belongs to the caller (internal/workplace); these
	// tests pass the same list it does.

	if _, err := w.UpsertBlock(BlockRow{
		ID: "b1", Workplace: "wp", Kind: "shell", Source: "engine", Input: "npm test",
		Status: "running", RunID: "run-1", PID: 42, LogPath: "/tmp/api.log", StartedAt: started,
	}, blockOutcomes); err != nil {
		t.Fatalf("UpsertBlock start: %v", err)
	}
	// The end arrives with only the outcome, the way a recorder sends it.
	merged, err := w.UpsertBlock(BlockRow{
		ID: "b1", Workplace: "wp", Kind: "shell", Source: "engine",
		Status: "completed", ExitCode: &code, EndedAt: started.Add(2 * time.Second),
	}, blockOutcomes)
	if err != nil {
		t.Fatalf("UpsertBlock end: %v", err)
	}
	if merged.Input != "npm test" || merged.RunID != "run-1" || merged.PID != 42 || merged.LogPath != "/tmp/api.log" {
		t.Fatalf("the end observation erased what the start recorded: %+v", merged)
	}
	if merged.Status != "completed" || merged.ExitCode == nil || *merged.ExitCode != 0 {
		t.Fatalf("the outcome did not land: %+v", merged)
	}
	if merged.Seq != 1 {
		t.Fatalf("seq moved on update: %d", merged.Seq)
	}

	// A late "running" observation must not undo an outcome — the two ends of
	// a piece of work may arrive in either order.
	late, err := w.UpsertBlock(BlockRow{
		ID: "b1", Workplace: "wp", Kind: "shell", Source: "engine",
		Status: "running", Input: "npm test", StartedAt: started,
	}, blockOutcomes)
	if err != nil {
		t.Fatalf("UpsertBlock late start: %v", err)
	}
	if late.Status != "completed" {
		t.Fatalf("a late start overwrote the outcome: %q", late.Status)
	}

	blocks, err := w.Blocks("wp", 0)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("upserting one id produced %d blocks", len(blocks))
	}
}

func TestWorkplaceBlockIdsAreScopedToOneWorkplace(t *testing.T) {
	s := openTemp(t)
	w := s.Workplaces()
	for _, id := range []string{"one", "two"} {
		if err := w.Create(WorkplaceRow{ID: id, Worktree: "/" + id, Name: "default", Active: true}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	if _, err := w.AppendBlock(BlockRow{ID: "b", Workplace: "one", Kind: "shell", Status: "running"}); err != nil {
		t.Fatalf("AppendBlock: %v", err)
	}
	if _, err := w.AppendBlock(BlockRow{ID: "b", Workplace: "one", Kind: "shell", Status: "running"}); !errors.Is(err, ErrBlockExists) {
		t.Fatalf("a second Append of one id = %v, want ErrBlockExists", err)
	}
	if _, err := w.UpsertBlock(BlockRow{ID: "b", Workplace: "two", Kind: "shell", Status: "running"}, blockOutcomes); !errors.Is(err, ErrBlockElsewhere) {
		t.Fatalf("moving a block between workplaces = %v, want ErrBlockElsewhere", err)
	}
}

func TestCloseBlocksByRunOnlyClosesRunningBlocks(t *testing.T) {
	s := openTemp(t)
	w := s.Workplaces()
	if err := w.Create(WorkplaceRow{ID: "wp", Worktree: "/w", Name: "default", Active: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	started := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	for _, row := range []BlockRow{
		{ID: "running", Workplace: "wp", Kind: "shell", Status: "running", RunID: "run-1", StartedAt: started},
		{ID: "done", Workplace: "wp", Kind: "shell", Status: "completed", RunID: "run-1", StartedAt: started},
		{ID: "other", Workplace: "wp", Kind: "shell", Status: "running", RunID: "run-2", StartedAt: started},
	} {
		if _, err := w.AppendBlock(row); err != nil {
			t.Fatalf("AppendBlock %s: %v", row.ID, err)
		}
	}
	code := 3
	closed, err := w.CloseBlocksByRun("run-1", "failed", &code, started.Add(time.Minute), blockOutcomes)
	if err != nil {
		t.Fatalf("CloseBlocksByRun: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed %d blocks, want 1", closed)
	}
	blocks, err := w.Blocks("wp", 0)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	byID := map[string]BlockRow{}
	for _, b := range blocks {
		byID[b.ID] = b
	}
	if got := byID["running"]; got.Status != "failed" || got.ExitCode == nil || *got.ExitCode != 3 || got.EndedAt.IsZero() {
		t.Fatalf("the running block was not closed: %+v", got)
	}
	if got := byID["done"]; got.Status != "completed" || got.ExitCode != nil {
		t.Fatalf("an already-closed block was rewritten: %+v", got)
	}
	if got := byID["other"]; got.Status != "running" {
		t.Fatalf("a block of another run was closed: %+v", got)
	}
}

func TestWorkplaceSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w := first.Workplaces()
	if err := w.Create(WorkplaceRow{ID: "wp", Worktree: "/w", Name: "spike", Active: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.AppendBlock(BlockRow{ID: "b1", Workplace: "wp", Kind: "agent", Source: "engine", Input: "fix the build", Status: "failed"}); err != nil {
		t.Fatalf("AppendBlock: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()
	rows, err := second.Workplaces().List("/w")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "spike" || !rows[0].Active || rows[0].Blocks != 1 {
		t.Fatalf("workplace did not survive the reopen: %+v", rows)
	}
	blocks, err := second.Workplaces().Blocks("wp", 0)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Input != "fix the build" || blocks[0].Kind != "agent" {
		t.Fatalf("block did not survive the reopen: %+v", blocks)
	}
}
