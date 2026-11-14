package cmd

import (
	"context"
	"fmt"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"os"
)

type storageOwnerRead func(context.Context, string) (rpc.RuntimeStorageReadResult, bool, error)

// storedRead never autostarts a host. A reached owner's failure is authoritative;
// only an absent owner permits offline access to the same database, never a copy.
func storedRead[T any](ctx context.Context, path, section string, direct bool, owner storageOwnerRead, decode func(rpc.RuntimeStorageReadResult) (T, error), local func(*store.Store) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !direct {
		result, reached, err := owner(ctx, section)
		if reached {
			if err != nil {
				return zero, err
			}
			if result.Section != section {
				return zero, fmt.Errorf("Runtime Service returned storage section %q, expected %q", result.Section, section)
			}
			return decode(result)
		}
	}
	db, err := store.OpenReadOnly(path)
	if os.IsNotExist(err) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	defer db.Close()
	return local(db)
}
