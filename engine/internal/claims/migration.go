package claims

import (
	_ "embed"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// Version 006 belongs to claims. The store reserves the number and this
// package supplies the schema, so a binary that never links claims never grows
// the table. Any store built with claims linked — the daemon, and the CLI
// through it — applies it on open.
//
// The store applies every registered version that has no schema_version row,
// so a database already at 006 can still receive 005 later. Both migrations
// ship in the same binary from the first release that has either; the gap only
// exists in a development build of one branch alone.
//
//go:embed 006_claims.sql
var claimsMigration string

func init() {
	store.RegisterMigration(store.VersionClaims, "claims", claimsMigration)
}
