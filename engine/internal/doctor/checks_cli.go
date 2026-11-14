package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// checkCLIOnPath answers "is the oberth I just ran the one PATH would find?".
// A second install earlier on PATH is the most confusing state a user can be
// in: every hint the CLI prints would name a binary they are not running.
func checkCLIOnPath(_ context.Context, env *Env) rpc.DoctorCheck {
	running, err := env.Executable()
	if err != nil {
		return warn("could not resolve the running binary", err.Error(), "")
	}
	found, err := env.LookPath(executableName(env.GOOS))
	if err != nil {
		return fail("oberth is not on PATH",
			fmt.Sprintf("running %s, but PATH has no oberth: %v", running, err),
			fmt.Sprintf("put %s on PATH — add `export PATH=\"%s:$PATH\"` to your shell's startup file, or run `mage install` in a checkout (it writes that line)",
				filepath.Dir(running), filepath.Dir(running)))
	}
	running, found = canonicalPath(running), canonicalPath(found)
	if sameExecutablePath(env.GOOS, running, found) {
		return ok("oberth resolves from PATH", found)
	}
	return warn("another oberth is first on PATH",
		fmt.Sprintf("PATH resolves oberth to %s; you are running %s", found, running),
		fmt.Sprintf("remove %s, or put %s earlier on PATH", found, filepath.Dir(running)))
}

// executableName is what PATH must contain on this platform.
func executableName(goos string) string {
	if goos == "windows" {
		return "oberth.exe"
	}
	return "oberth"
}

// canonicalPath follows symlinks when it can. A path that cannot be resolved is
// still worth printing, so it comes back unchanged.
func canonicalPath(path string) string {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return target
}

// sameExecutablePath compares resolved paths with the platform's case rules.
func sameExecutablePath(goos, left, right string) bool {
	switch goos {
	case "windows", "darwin":
		return strings.EqualFold(left, right)
	}
	return left == right
}
