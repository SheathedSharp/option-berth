// Package buildinfo carries what this build knows about itself: the version
// stamped in at link time and the platform it was built for.
//
// It is deliberately separate from any update mechanism. option-berth is built
// from source, and nothing in this tree may replace the running executable with
// a binary downloaded from somewhere else — see UPSTREAM.md for why the fork
// removed that machinery instead of repointing it.
package buildinfo

import (
	"fmt"
	"runtime"
	"strings"
)

// Set via -ldflags at build time. `mage build` stamps all three; a bare
// `go build` leaves them at these values so a local build is recognisable.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// VersionString returns the detailed one-line version string used by the
// version subcommand.
func VersionString() string {
	return fmt.Sprintf("option-berth %s (%s/%s)", VersionValue(), runtime.GOOS, runtime.GOARCH)
}

// VersionValue returns the normalized SemVer value stamped into this build.
// Development builds keep their explicit `dev` marker.
func VersionValue() string {
	version := Version
	if version != "dev" && version != "unknown" && !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return version
}
