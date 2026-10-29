// Package buildinfo carries what this build knows about itself: the version
// stamped in at link time and the platform it was built for.
//
// It is deliberately separate from the update check. `oberth version --check`
// only reads the latest published tag; nothing in this tree downloads a binary
// or replaces the running executable — see UPSTREAM.md for why the fork removed
// that machinery instead of repointing it.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
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
//
// `go install module@version` does not apply this package's link flags, so the
// stamped value stays "dev". In that case the module version is still available
// through the build info and is what a user expects `oberth version` to show.
// Development builds keep their explicit `dev` marker.
func VersionValue() string {
	return versionValue(Version, debug.ReadBuildInfo)
}

func versionValue(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	version := stamped
	if version == "dev" || version == "unknown" {
		if info, ok := readBuildInfo(); ok && info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
	}
	if version != "dev" && version != "unknown" && !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return version
}
