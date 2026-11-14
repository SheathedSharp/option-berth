package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/doctor"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"os"
	"path/filepath"
	"time"
)

func existingStorageOwner(ctx context.Context, section string) (rpc.RuntimeStorageReadResult, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	expectedPath, err := filepath.Abs(store.Path())
	if err != nil {
		return rpc.RuntimeStorageReadResult{}, true, err
	}
	c, err := client.Dial(ctx, client.ClientInfo{Name: "cli", Version: buildinfo.Version})
	if err != nil {
		return rpc.RuntimeStorageReadResult{}, !errors.Is(err, client.ErrNotRunning), err
	}
	defer c.Close()
	var result rpc.RuntimeStorageReadResult
	err = c.Call(ctx, "runtime.storage.read", rpc.RuntimeStorageReadParams{Section: section, ExpectedDBPath: expectedPath}, &result)
	return result, true, err
}
func storageReadError(err error) error {
	if errors.Is(err, store.ErrStorageOwned) {
		return failHint("runtime_service_busy", err.Error(), "omit --no-runtime-service to read through the active matching Runtime Service; close previous hosts explicitly before switching")
	}
	return err
}
func storedRunExits(ctx context.Context) ([]store.RunExitRow, error) {
	return storedRead(ctx, store.Path(), "run_exits", noRuntimeServiceFlag, existingStorageOwner,
		func(result rpc.RuntimeStorageReadResult) ([]store.RunExitRow, error) {
			if result.RunExits == nil {
				return nil, fmt.Errorf("Runtime Service omitted run-exit read receipt")
			}
			out := make([]store.RunExitRow, 0, len(*result.RunExits))
			for _, row := range *result.RunExits {
				out = append(out, store.RunExitRow(row))
			}
			return out, nil
		}, func(db *store.Store) ([]store.RunExitRow, error) { return db.RunExitsContext(ctx, 0) })
}
func storedWorktrees(ctx context.Context) ([]store.WorktreeIdentity, error) {
	return storedRead(ctx, store.Path(), "worktrees", noRuntimeServiceFlag, existingStorageOwner,
		func(result rpc.RuntimeStorageReadResult) ([]store.WorktreeIdentity, error) {
			if result.Worktrees == nil {
				return nil, fmt.Errorf("Runtime Service omitted worktree read receipt")
			}
			out := make([]store.WorktreeIdentity, 0, len(*result.Worktrees))
			for _, row := range *result.Worktrees {
				out = append(out, store.WorktreeIdentity(row))
			}
			return out, nil
		}, func(db *store.Store) ([]store.WorktreeIdentity, error) { return db.WorktreeIdentitiesContext(ctx) })
}
func doctorStorageInfo(ctx context.Context) (rpc.RuntimeStorageSchema, error) {
	info, err := storedRead(ctx, store.Path(), "schema", false, existingStorageOwner,
		func(result rpc.RuntimeStorageReadResult) (rpc.RuntimeStorageSchema, error) {
			if result.Schema == nil {
				return rpc.RuntimeStorageSchema{}, fmt.Errorf("Runtime Service omitted schema read receipt")
			}
			return *result.Schema, nil
		}, func(db *store.Store) (rpc.RuntimeStorageSchema, error) { return doctor.InspectDatabase(ctx, db) })
	if err == nil && info.Path == "" {
		return info, &os.PathError{Op: "stat", Path: store.Path(), Err: os.ErrNotExist}
	}
	return info, err
}
