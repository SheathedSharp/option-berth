package usersvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shim puts a fake supervisor command first on PATH and returns the file it
// appends its argv to. The real launchctl and systemctl are never run: the
// package resolves them through PATH precisely so a test can stand in for them.
//
// body is the shell the fake runs after logging one line per invocation, so a
// test can make it answer differently per domain.
func shim(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SHIM_LOG\"\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the %s shim: %v", name, err)
	}
	t.Setenv("SHIM_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// calls reads the argv lines the fake supervisor logged.
func calls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading the shim log: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// wantCalls compares what the supervisor was asked to do, in order.
func wantCalls(t *testing.T, log string, want ...string) {
	t.Helper()
	got := calls(t, log)
	if len(got) != len(want) {
		t.Fatalf("supervisor calls = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("supervisor call %d = %q, want %q (all: %q)", i, got[i], want[i], got)
		}
	}
}
