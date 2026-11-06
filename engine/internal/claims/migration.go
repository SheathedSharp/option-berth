package claims

import (
	_ "embed"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// The reservations table is created by the package that owns it instead of
// shipping inside internal/store, so a build that never links claims never grows
// the table. store.RegisterMigration exists for exactly this: the reserved
// version numbers belong to sibling packages, and those packages fill them in
// from their own init.
//
//go:embed 006_claims.sql
var reservationsSchema string

func init() {
	const name = "claims"
	store.RegisterMigration(store.VersionClaims, name, reservationsSchema)
}
