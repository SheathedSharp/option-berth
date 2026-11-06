package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// checkCLIOnPath answers "is the oberth I just ran the oberth PATH would find?".
// A second install earlier on PATH is the single most confusing thing a user
// can have: every hint the CLI prints then names a binary they are not running.
func checkCLIOnPath(_ context.Context, env *Env) rpc.DoctorCheck {
	exe, err := env.Executable()
	if err != nil {
		return warn("could not resolve the running binary", err.Error(), "")
	}
	exe = resolveLinks(exe)

	onPath, err := env.LookPath(binaryName(env.GOOS))
	if err != nil {
		return fail("oberth is not on PATH",
			fmt.Sprintf("running %s, but PATH has no oberth: %v", exe, err),
			fmt.Sprintf("put %s on PATH — add `export PATH=\"%s:$PATH\"` to your shell's startup file, or run `mage install` in a checkout (it writes that line)",
				filepath.Dir(exe), filepath.Dir(exe)))
	}
	onPath = resolveLinks(onPath)

	if samePath(env.GOOS, exe, onPath) {
		return ok("oberth resolves from PATH", onPath)
	}
	return warn("another oberth is first on PATH",
		fmt.Sprintf("PATH resolves oberth to %s; you are running %s", onPath, exe),
		fmt.Sprintf("remove %s, or put %s earlier on PATH", onPath, filepath.Dir(exe)))
}

func binaryName(goos string) string {
	if goos == "windows" {
		return "oberth.exe"
	}
	return "oberth"
}

// resolveLinks follows symlinks when it can and returns the input otherwise:
// two spellings of one file must compare equal, but a path that cannot be
// resolved is still worth printing.
func resolveLinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// samePath compares two resolved paths with the platform's case rules.
func samePath(goos, a, b string) bool {
	if goos == "windows" || goos == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
