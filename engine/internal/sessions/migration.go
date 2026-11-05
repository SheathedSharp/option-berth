package sessions

import (
	_ "embed"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// Sessions own migration version 005. The store reserves the number and this
// package supplies the schema, so only a binary that links sessions ever
// creates the table. The store separately asserts that its own embedded
// migrations never claim a reserved version.
//
//go:embed migrations/005_sessions.sql
var sessionsMigration string

func init() {
	store.RegisterMigration(store.VersionSessions, "sessions", sessionsMigration)
}
