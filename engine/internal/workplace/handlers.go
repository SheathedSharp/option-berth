package workplace

import (
	"context"
	"fmt"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// The daemon side of the workplace record. The package owns the namespace and
// registers it from its own init(), the way internal/doctor owns daemon.doctor
// and internal/daemon/runsreg owns runs.*, so internal/daemon never imports
// this package.
func init() {
	daemon.RegisterHandler("workplace.list", handleList)
	daemon.RegisterHandler("workplace.new", handleNew)
	daemon.RegisterHandler("workplace.use", handleUse)
	daemon.RegisterHandler("workplace.show", handleShow)
	daemon.RegisterHandler("workplace.delete", handleDelete)
	daemon.RegisterHandler("workplace.record", handleRecord)
	daemon.RegisterCapability("workplace")
}

// failure renders an error from this package the way the wire expects: the
// code and the next step come from the package's one mapping, never from the
// handler.
func failure(err error) error {
	return rpc.NewError(Code(err), err.Error(), Hint(err))
}

// storeFor is the daemon's database, or the error every database-backed method
// reports when it came up without one.
func storeFor(req *daemon.Request) (*store.Store, error) {
	if req.Runtime == nil || req.Runtime.Store == nil {
		return nil, rpc.NewError(rpc.CodeInternal, "this daemon has no database open",
			"check `oberth daemon log` for the error opening option-berth.db, then `oberth daemon restart`")
	}
	return req.Runtime.Store, nil
}

// rootOf is the worktree a call is about, in the one spelling the tables use.
// Every method takes the path from its caller and canonicalizes it here, so a
// client that resolved the path differently still reaches the same rows.
func rootOf(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", rpc.NewError(rpc.CodeInvalidParams, "worktree is required",
			"send the worktree root the call is about")
	}
	root, err := store.CleanRoot(path)
	if err != nil {
		return "", rpc.NewError(rpc.CodeInvalidParams, "worktree is not a usable path: "+path, "")
	}
	return root, nil
}

func handleList(_ context.Context, req *daemon.Request) (any, error) {
	var p rpc.WorkplaceParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	root, err := rootOf(p.Worktree)
	if err != nil {
		return nil, err
	}
	st, err := storeFor(req)
	if err != nil {
		return nil, err
	}
	out, err := ListResult(st, root)
	if err != nil {
		return nil, failure(err)
	}
	return out, nil
}

func handleNew(_ context.Context, req *daemon.Request) (any, error) {
	var p rpc.WorkplaceNewParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	root, err := rootOf(p.Worktree)
	if err != nil {
		return nil, err
	}
	st, err := storeFor(req)
	if err != nil {
		return nil, err
	}
	name := DefaultName
	if strings.TrimSpace(p.Name) != "" {
		if name, err = ValidateName(p.Name); err != nil {
			return nil, failure(err)
		}
	}
	all, err := List(st, root)
	if err != nil {
		return nil, failure(err)
	}
	for _, w := range all {
		if strings.EqualFold(w.Name, name) {
			return nil, failure(fmt.Errorf("%w: %q", ErrConflict, w.Name))
		}
	}
	created, err := Create(st, root, name)
	if err != nil {
		return nil, failure(err)
	}
	return rpc.WorkplaceNewResult{Workplace: Wire(created)}, nil
}

func handleUse(_ context.Context, req *daemon.Request) (any, error) {
	var p rpc.WorkplaceUseParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	root, err := rootOf(p.Worktree)
	if err != nil {
		return nil, err
	}
	st, err := storeFor(req)
	if err != nil {
		return nil, err
	}
	_, target, err := Find(st, root, p.Selector)
	if err != nil {
		return nil, failure(err)
	}
	if !target.Active {
		if err := st.Workplaces().SetActive(root, target.ID); err != nil {
			return nil, failure(fmt.Errorf("switching workplace: %w", err))
		}
		target.Active = true
	}
	return rpc.WorkplaceUseResult{Workplace: Wire(target)}, nil
}

func handleShow(_ context.Context, req *daemon.Request) (any, error) {
	var p rpc.WorkplaceShowParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	root, err := rootOf(p.Worktree)
	if err != nil {
		return nil, err
	}
	st, err := storeFor(req)
	if err != nil {
		return nil, err
	}
	out, err := ShowResult(st, root, p.Selector, p.Limit)
	if err != nil {
		return nil, failure(err)
	}
	return out, nil
}

func handleDelete(_ context.Context, req *daemon.Request) (any, error) {
	var p rpc.WorkplaceDeleteParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	root, err := rootOf(p.Worktree)
	if err != nil {
		return nil, err
	}
	st, err := storeFor(req)
	if err != nil {
		return nil, err
	}
	_, target, err := Find(st, root, p.Selector)
	if err != nil {
		return nil, failure(err)
	}
	gone, next, err := st.Workplaces().Delete(target.ID)
	if err != nil {
		return nil, failure(fmt.Errorf("deleting the workplace: %w", err))
	}
	if !gone {
		return nil, failure(fmt.Errorf("%w: %q", ErrNotFound, target.Name))
	}
	out := rpc.WorkplaceDeleteResult{Deleted: target.ID}
	if next != "" {
		// The workplace that took over is read back rather than assembled from
		// the id, so the answer carries its name and block count too.
		if row, ok, err := st.Workplaces().Get(next); err == nil && ok {
			w := Wire(FromRow(row))
			out.Active = &w
		}
	}
	return out, nil
}

func handleRecord(_ context.Context, req *daemon.Request) (any, error) {
	var p rpc.WorkplaceRecordParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	root, err := rootOf(p.Worktree)
	if err != nil {
		return nil, err
	}
	st, err := storeFor(req)
	if err != nil {
		return nil, err
	}
	rec, err := Decode(p.Block)
	if err != nil {
		return nil, failure(err)
	}
	where, err := Target(st, root)
	if err != nil {
		return nil, failure(err)
	}
	row, err := st.Workplaces().UpsertBlock(rec.Row(where.ID), TerminalStatuses())
	if err != nil {
		return nil, failure(fmt.Errorf("recording the block: %w", err))
	}
	// The workplace is read back rather than reported as it was before the
	// write: a caller that just added a block is asking what the record looks
	// like now, and a stale count would be the one thing it came to find out.
	if after, ok, err := st.Workplaces().Get(where.ID); err == nil && ok {
		where = FromRow(after)
	}
	return rpc.WorkplaceRecordResult{
		Workplace: Wire(where),
		Block:     BlockWire(BlockFromRow(row)),
	}, nil
}
