//go:build !windows

package ports

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// shimLogEnv names the file the shims append their own invocation to. It is an
// environment variable rather than a path baked into the script so the script
// never has to quote a temporary directory.
const shimLogEnv = "OBERTH_TEST_SHIM_LOG"

// collectorShim writes an executable named name into dir. The script records its
// own name and exact argv in the shared log before printing body, so a test can
// see which collector ran, in what order, and with which arguments.
//
// It uses /bin/sh by absolute path and only shell builtins: the test points PATH
// at the shim directory alone, so anything else would fail to resolve.
func collectorShim(t *testing.T, dir, name, body string) {
	t.Helper()
	log := func(format string) string {
		return `printf ` + format + ` >> "$` + shimLogEnv + `"` + "\n"
	}
	script := "#!/bin/sh\n" +
		log(`'`+name+`'`) +
		`for arg in "$@"; do ` + log(`' %s' "$arg"`) + `done` + "\n" +
		log(`'\n'`) +
		body
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// collectorCalls reads the shim log back as one "name arg arg" line per exec.
func collectorCalls(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no collector ran: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// The Darwin fallback is a second exec, not a second decoder: issue #156 is a
// netstat that exits 0 without a TCP table, and only a real child process can
// show that the fallback is actually spawned with the arguments lsof needs.
// Nothing here injects the listenerCommand seam; the scan resolves both
// collectors through PATH exactly as a status read does.
func TestDarwinFallbackRunsThroughRealExec(t *testing.T) {
	shimDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "collectors.log")
	t.Setenv(shimLogEnv, logPath)
	// A PATH holding the two shims and nothing else: the scan may not borrow the
	// host's own netstat or lsof, which would make the result a fact about this
	// machine instead of about the fallback.
	t.Setenv("PATH", shimDir)

	// netstat exits 0 with no table at all. That is the shape of issue #156 and
	// one of the shapes that falls back: a printed header is a decodable table
	// with zero rows, which the scan reports as a healthy empty observation
	// rather than as a reason to switch collectors.
	collectorShim(t, shimDir, "netstat", "exit 0\n")
	// lsof's -Fpcltn record stream: pid, command, descriptor, family, endpoint.
	collectorShim(t, shimDir, "lsof", "printf 'p900\\ncrelayd\\nf3\\ntIPv4\\nn127.0.0.1:18123\\n'\n")

	rows, source, err := collectListenersSource("darwin", executeListenerCommand)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	calls := collectorCalls(t, logPath)
	want := []string{
		"netstat -anvl -p tcp",
		"lsof -a -n -P -w +c 0 -iTCP -sTCP:LISTEN -Fpcltn",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("collectors = %q\nwant       %q", calls, want)
	}
	if source != listenerSourceLsofName {
		t.Fatalf("source = %q, want %q", source, listenerSourceLsofName)
	}
	wantRows := []ListeningPort{{Port: 18123, PID: 900, Process: "relayd", BindAddress: "127.0.0.1", IPVersion: "IPv4"}}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("rows = %+v\nwant   %+v", rows, wantRows)
	}
}
