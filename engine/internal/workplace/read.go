package workplace

import (
	"fmt"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// The reads behind the workplace methods, written once.
//
// The daemon serves them over RPC and the CLI runs them itself when there is
// no daemon to ask, so both answer a question about a worktree from the same
// rules: which workplace is active, what a selector resolves to, and how a
// truncated record is reported. Only the transport differs.

// List reads a worktree's workplaces, oldest first.
//
// A nil store means there is no database file at all: a machine that has never
// recorded anything. That is the same answer as an empty record — nothing has
// been done here — and not a failure to read one, so it is not reported as an
// error. A database that exists but cannot be read still is.
func List(st *store.Store, root string) ([]Workplace, error) {
	if st == nil {
		return nil, nil
	}
	rows, err := st.Workplaces().List(root)
	if err != nil {
		return nil, fmt.Errorf("reading workplaces: %w", err)
	}
	out := make([]Workplace, 0, len(rows))
	for _, r := range rows {
		out = append(out, FromRow(r))
	}
	return out, nil
}

// Find reads the worktree's workplaces and resolves a selector against them.
// The whole list comes back too: a caller that is about to switch or delete
// usually needs to say what is left afterwards.
func Find(st *store.Store, root, selector string) ([]Workplace, Workplace, error) {
	all, err := List(st, root)
	if err != nil {
		return nil, Workplace{}, err
	}
	if len(all) == 0 {
		return nil, Workplace{}, ErrNoWorkplace
	}
	chosen, err := Resolve(all, selector)
	if err != nil {
		return nil, Workplace{}, err
	}
	return all, chosen, nil
}

// Target is the workplace new work lands in: the active one, or — when the
// worktree has workplaces but none is marked active — the newest, which is the
// one Delete would have chosen, so there is one rule rather than two. A
// worktree with no workplace at all gets the default one, because recording
// work is the thing a workplace exists to hold.
//
// This is the one function here that writes, and it is called by recording
// only: looking at the record must not bring a workplace into being.
func Target(st *store.Store, root string) (Workplace, error) {
	if st == nil {
		return Workplace{}, fmt.Errorf("recording needs the daemon's database, and there is none")
	}
	all, err := List(st, root)
	if err != nil {
		return Workplace{}, err
	}
	if w, ok := Active(all); ok {
		return w, nil
	}
	if len(all) > 0 {
		newest := all[len(all)-1]
		if err := st.Workplaces().SetActive(root, newest.ID); err != nil {
			return Workplace{}, fmt.Errorf("activating a workplace: %w", err)
		}
		newest.Active = true
		return newest, nil
	}
	return Create(st, root, DefaultName)
}

// Create adds a workplace to a worktree and makes it active. It reads the row
// back rather than assembling one, so the answer carries the timestamps the
// store gave it and the block count it actually has.
func Create(st *store.Store, root, name string) (Workplace, error) {
	id := NewID()
	if err := st.Workplaces().Create(store.WorkplaceRow{ID: id, Worktree: root, Name: name, Active: true}); err != nil {
		return Workplace{}, fmt.Errorf("creating the workplace: %w", err)
	}
	row, ok, err := st.Workplaces().Get(id)
	if err != nil || !ok {
		return Workplace{ID: id, Worktree: root, Name: name, Active: true}, nil
	}
	return FromRow(row), nil
}

// ListResult is the answer to workplace.list and to `oberth workplace list`
// without a daemon.
func ListResult(st *store.Store, root string) (rpc.WorkplaceListResult, error) {
	all, err := List(st, root)
	if err != nil {
		return rpc.WorkplaceListResult{}, err
	}
	out := rpc.WorkplaceListResult{Worktree: root, Workplaces: make([]rpc.Workplace, 0, len(all))}
	for _, w := range all {
		out.Workplaces = append(out.Workplaces, Wire(w))
		if w.Active {
			out.Active = w.ID
		}
	}
	return out, nil
}

// ShowResult is the answer to workplace.show and to `oberth workplace show`
// without a daemon. An empty selector means the workplace the worktree is on;
// limit above zero caps how many of the newest blocks come back.
//
// Reading never changes anything: no selector picks the active workplace, or
// the newest one when none is marked active — the same one Delete would have
// chosen — and a worktree with no workplace at all is ErrNoWorkplace rather
// than a reason to create one. Recording is what creates a workplace; looking
// at the record is not.
func ShowResult(st *store.Store, root, selector string, limit int) (rpc.WorkplaceShowResult, error) {
	if limit < 0 {
		return rpc.WorkplaceShowResult{}, fmt.Errorf("%w: limit cannot be negative", ErrInvalid)
	}
	if strings.TrimSpace(selector) == "" {
		all, err := List(st, root)
		if err != nil {
			return rpc.WorkplaceShowResult{}, err
		}
		if len(all) == 0 {
			return rpc.WorkplaceShowResult{}, ErrNoWorkplace
		}
		if active, ok := Active(all); ok {
			selector = active.ID
		} else {
			selector = all[len(all)-1].ID
		}
	}
	_, chosen, err := Find(st, root, selector)
	if err != nil {
		return rpc.WorkplaceShowResult{}, err
	}
	rows, err := st.Workplaces().Blocks(chosen.ID, limit)
	if err != nil {
		return rpc.WorkplaceShowResult{}, fmt.Errorf("reading the workplace record: %w", err)
	}
	count, err := st.Workplaces().BlockCount(chosen.ID)
	if err != nil {
		return rpc.WorkplaceShowResult{}, fmt.Errorf("counting the workplace record: %w", err)
	}
	out := rpc.WorkplaceShowResult{
		Workplace: Wire(chosen),
		Blocks:    make([]rpc.WorkplaceBlock, 0, len(rows)),
		Truncated: count > len(rows),
	}
	for _, r := range rows {
		out.Blocks = append(out.Blocks, BlockWire(BlockFromRow(r)))
	}
	return out, nil
}

// Wire is a workplace in its wire shape.
func Wire(w Workplace) rpc.Workplace {
	return rpc.Workplace{
		ID: w.ID, Name: w.Name, Worktree: w.Worktree, Active: w.Active, Blocks: w.Blocks,
		CreatedAt: rfc3339(w.CreatedAt), UpdatedAt: rfc3339(w.UpdatedAt),
	}
}

// BlockWire is a block in its wire shape.
func BlockWire(b Block) rpc.WorkplaceBlock {
	out := rpc.WorkplaceBlock{
		ID: b.ID, Workplace: b.Workplace, Seq: b.Seq,
		Kind: string(b.Kind), Source: string(b.Source), Input: b.Input, Status: string(b.Status),
		Provider: b.Provider, RunID: b.RunID, PID: b.PID, LogPath: b.LogPath, Output: b.Output,
		ExitCode: b.ExitCode, StartedAt: rfc3339(b.StartedAt),
	}
	if !b.EndedAt.IsZero() {
		out.EndedAt = rfc3339(b.EndedAt)
	}
	return out
}

// Decode turns a wire block into a validated record.
func Decode(in rpc.WorkplaceBlockInput) (Record, error) {
	rec := Record{
		ID: in.ID, Kind: Kind(in.Kind), Source: Source(in.Source), Input: in.Input,
		Status: Status(in.Status), Provider: in.Provider, RunID: in.RunID, PID: in.PID,
		LogPath: in.LogPath, Output: in.Output, ExitCode: in.ExitCode,
	}
	var err error
	if rec.StartedAt, err = parseTime(in.StartedAt, "started_at"); err != nil {
		return Record{}, err
	}
	if rec.EndedAt, err = parseTime(in.EndedAt, "ended_at"); err != nil {
		return Record{}, err
	}
	if err := rec.Validate(); err != nil {
		return Record{}, err
	}
	return rec.WithDefaults(), nil
}

// parseTime reads an RFC 3339 timestamp. An unreadable one is refused rather
// than dropped: a record carrying the wrong time is worse than a refused call,
// because nothing downstream can tell that it is wrong.
func parseTime(value, field string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s is not an RFC 3339 timestamp: %q", ErrInvalid, field, value)
	}
	return t.UTC(), nil
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
