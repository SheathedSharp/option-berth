package runtimesvc

import (
	"context"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"os"
)

func init() {
	RegisterHandler("runtime.storage.read", handleStorageRead)
	RegisterCapability("runtime.storage.read")
}
func handleStorageRead(ctx context.Context, req *Request) (any, error) {
	var params rpc.RuntimeStorageReadParams
	if err := req.Bind(&params); err != nil {
		return nil, err
	}
	return readStorage(ctx, req.HostContext.Store, params)
}

// readStorage is shared by the production handler and isolated Store tests.
func readStorage(ctx context.Context, db *store.Store, params rpc.RuntimeStorageReadParams) (rpc.RuntimeStorageReadResult, error) {
	out := rpc.RuntimeStorageReadResult{Section: params.Section}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if db == nil {
		return out, rpc.NewError(rpc.CodeBusy, "Runtime Service storage is unavailable", "preserve the database and inspect startup diagnostics")
	}
	switch params.Section {
	case "schema":
		version, err := db.VersionContext(ctx)
		if err != nil {
			return out, err
		}
		info, err := os.Stat(db.DBPath())
		if err != nil {
			return out, err
		}
		out.Schema = &rpc.RuntimeStorageSchema{Path: db.DBPath(), Version: version, Size: info.Size()}
	case "worktrees":
		rows, err := db.WorktreeIdentitiesContext(ctx)
		if err != nil {
			return out, err
		}
		result := make([]rpc.RuntimeStorageWorktree, 0, len(rows))
		for _, row := range rows {
			result = append(result, rpc.RuntimeStorageWorktree(row))
		}
		out.Worktrees = &result
	case "run_exits":
		rows, err := db.RunExitsContext(ctx, 0)
		if err != nil {
			return out, err
		}
		result := make([]rpc.RuntimeStorageRunExit, 0, len(rows))
		for _, row := range rows {
			result = append(result, rpc.RuntimeStorageRunExit(row))
		}
		out.RunExits = &result
	default:
		return out, rpc.NewError(rpc.CodeInvalidParams, "unknown storage read section", "use schema, worktrees, or run_exits")
	}
	return out, nil
}
