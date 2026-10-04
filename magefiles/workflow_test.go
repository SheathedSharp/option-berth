//go:build mage

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mageFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "engine"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "engine", "go.mod"), []byte("module example.invalid/fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("1.2.3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("HOME", t.TempDir())
	return root
}

func TestRenderingNeverBuildsMissingClient(t *testing.T) {
	root := mageFixture(t)
	scope := "services:demo"
	for _, call := range []func() error{Snapshot, States, Window, func() error { return Shot(&scope) }, func() error { return Service("demo") }} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "client is not built") {
			t.Fatalf("expected explicit build instruction, got %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "bin")); !os.IsNotExist(err) {
		t.Fatal("render implicitly built binaries")
	}
	if _, err := os.Stat(filepath.Join(root, "client")); !os.IsNotExist(err) {
		t.Fatal("render created client output without a binary")
	}
}

func TestStopNeverBuildsMissingCLI(t *testing.T) {
	root := mageFixture(t)
	if err := Stop(); err == nil {
		t.Fatal("stop succeeded without an available CLI")
	}
	if _, err := os.Stat(filepath.Join(root, "bin")); !os.IsNotExist(err) {
		t.Fatal("stop implicitly built a CLI")
	}
}

func TestSnapshotScopeCannotEscapeCache(t *testing.T) {
	for _, scope := range []string{"services:demo", "services:../../outside", `services:..\outside`, "代码:商店"} {
		name := snapshotFile(scope)
		if filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
			t.Fatalf("unsafe filename: %q", name)
		}
	}
}

func TestVersionStillComesFromVersionFile(t *testing.T) {
	mageFixture(t)
	p, err := loadProject()
	if err != nil || p.version != "1.2.3" {
		t.Fatalf("version=%q error=%v", p.version, err)
	}
}

func TestRunCLIRejectsAmbiguousArgumentsBeforeBuild(t *testing.T) {
	root := mageFixture(t)
	err := RunCLI(`status --project 'project with spaces'`)
	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("expected exact-argv guidance before build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bin")); !os.IsNotExist(err) {
		t.Fatal("invalid arguments triggered a build")
	}
}

func TestSplitArgsPreservesExactJSONArgv(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  []string
	}{
		{"version --json", []string{"version", "--json"}},
		{`["status","--project","项目 with spaces"]`, []string{"status", "--project", "项目 with spaces"}},
		{`["logs","","C:\\work\\api","$HOME","$(echo no)"]`, []string{"logs", "", `C:\work\api`, "$HOME", "$(echo no)"}},
		{`[]`, []string{}}, {" ", nil},
	} {
		got, err := splitArgs(tc.input)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q: got=%q err=%v want=%q", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{`["unterminated"`, `["ok",1]`, `["ok"] trailing`, `status "broken`, `status C:\work`} {
		if _, err := splitArgs(input); err == nil {
			t.Fatalf("ambiguous or invalid argv accepted: %q", input)
		}
	}
}

func TestSplitArgsRejectsNullAndNUL(t *testing.T) {
	for _, input := range []string{`["ok",null]`, `["ok","\u0000"]`, "status\x00"} {
		if _, err := splitArgs(input); err == nil {
			t.Fatalf("invalid argv accepted: %q", input)
		}
	}
}
