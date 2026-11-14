package testenv

import "strings"

// runtimeServiceRunArg is the argument that makes a process a runtime service.
const runtimeServiceRunArg = " runtime service run"

// splitRuntimeServiceRun returns the executable of a `ps` command line whose first argument
// is `runtime service run`.
//
// `ps -axo command=` joins argv with single spaces and quotes nothing, so a path
// containing spaces cannot be recovered by splitting on whitespace: an
// executable at "/tmp/my dir/oberth" would look like the two arguments
// "/tmp/my" and "dir/oberth". The recoverable thing is the boundary — the
// executable is everything before the first " runtime service run" that stands as a whole
// argument rather than as part of a longer one.
func splitRuntimeServiceRun(cmdline string) (string, bool) {
	for at := 0; at < len(cmdline); {
		found := strings.Index(cmdline[at:], runtimeServiceRunArg)
		if found < 0 {
			return "", false
		}
		found += at
		next := found + len(runtimeServiceRunArg)
		if next == len(cmdline) || cmdline[next] == ' ' {
			return cmdline[:found], true
		}
		at = found + 1
	}
	return "", false
}
