package draft

import (
	"os"
	"path/filepath"
	"testing"
)

// installStub puts executable stubs on a directory of their own and returns
// it, so a test controls exactly what PATH contains.
func installStub(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSupportedMatrix(t *testing.T) {
	got := Supported()
	if len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Fatalf("Supported = %v, want [claude codex] in matrix order", got)
	}
	if _, ok := Lookup("claude"); !ok {
		t.Error("claude is not in the matrix")
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("an unknown name was found")
	}
}

func TestDetectReadsTheGivenPath(t *testing.T) {
	both := Detect([]string{"PATH=" + installStub(t, "claude", "codex")})
	if len(both) != 2 || both[0].Name != "claude" || both[1].Name != "codex" {
		t.Fatalf("Detect = %v, want both stubs in matrix order", both)
	}

	one := Detect([]string{"PATH=" + installStub(t, "codex")})
	if len(one) != 1 || one[0].Name != "codex" {
		t.Fatalf("Detect = %v, want just codex", one)
	}

	if none := Detect([]string{"PATH=" + t.TempDir()}); len(none) != 0 {
		t.Fatalf("Detect = %v, want none", none)
	}
}

func TestCLIArgvPutsThePromptLast(t *testing.T) {
	cli, ok := Lookup("claude")
	if !ok {
		t.Fatal("claude is not in the matrix")
	}
	got := cli.Argv([]string{"--allowedTools", "Bash"}, "sonnet", "the brief", nil)
	want := []string{"claude", "-p", "--model", "sonnet", "--allowedTools", "Bash", "the brief"}
	if len(got) != len(want) {
		t.Fatalf("Argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Argv[%d] = %q, want %q (program, flags, model, extra, then the prompt)", i, got[i], want[i])
		}
	}

	// No model asked for: no flag invented, and the prompt still lands last.
	got = cli.Argv(nil, "", "the brief", nil)
	want = []string{"claude", "-p", "the brief"}
	if len(got) != len(want) {
		t.Fatalf("Argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
