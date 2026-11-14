package rpc

import "github.com/sheathedsharp/option-berth/engine/internal/store"

// RuntimeStorageReadParams selects one explicit read from the authoritative
// owning Store. It never accepts SQL or opens a second database connection.
type RuntimeStorageReadParams struct {
	Section string `json:"section" jsonschema:"enum=schema,enum=worktrees,enum=run_exits"`
	// ExpectedDBPath binds a local reader to the database it selected.
	ExpectedDBPath string `json:"expected_db_path,omitempty"`
}
type RuntimeStorageSchema struct {
	Path    string `json:"path"`
	Version int    `json:"version"`
	Size    int64  `json:"size"`
}
type RuntimeStorageWorktree store.WorktreeIdentity
type RuntimeStorageRunExit store.RunExitRow

type RuntimeStorageReadResult struct {
	Section   string                    `json:"section"`
	Schema    *RuntimeStorageSchema     `json:"schema,omitempty"`
	Worktrees *[]RuntimeStorageWorktree `json:"worktrees,omitempty"`
	RunExits  *[]RuntimeStorageRunExit  `json:"run_exits,omitempty"`
}

func init() {
	Describe("runtime.storage.read", RuntimeStorageReadParams{}, RuntimeStorageReadResult{}, nil, nil)
}
