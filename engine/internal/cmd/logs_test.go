package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/runs"
)

func TestReadFileLogsReturnsTailAndSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := readFileLogs(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != path || !doc.Truncated {
		t.Fatalf("doc = %+v, want source %q and truncated output", doc, path)
	}
	want := []string{"two", "three"}
	if len(doc.Lines) != len(want) || doc.Lines[0] != want[0] || doc.Lines[1] != want[1] {
		t.Fatalf("lines = %#v, want %#v", doc.Lines, want)
	}
}

func TestReadFileLogsMissingFileIsNotFound(t *testing.T) {
	_, err := readFileLogs(filepath.Join(t.TempDir(), "missing.log"), 10)
	if err == nil {
		t.Fatal("readFileLogs returned nil for a missing file")
	}
	if got := exitCodeFor(err); got != exitFail {
		t.Fatalf("exit code = %d, want %d", got, exitFail)
	}
}

func TestLogMetadataReadsPortlessActiveRun(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if err := runs.Add(runs.Entry{PID: os.Getpid(), ID: "run-1", Group: "", Name: "worker"}); err != nil {
		t.Fatal(err)
	}

	meta := logMetadataForTarget(context.Background(), nil, "worker", nil)
	if meta.Service != "worker" || meta.RunID != "run-1" || meta.PID != os.Getpid() || meta.Status != "running" {
		t.Fatalf("metadata = %+v, want the active worker run", meta)
	}
}

func TestLogMetadataUsesPortRunAttribution(t *testing.T) {
	row := &ports.ListeningPort{PID: 44, RunID: "run-2", Tag: "api", RunRootPID: 42}
	meta := logMetadataForTarget(context.Background(), nil, "3000", row)
	if meta.Service != "api" || meta.RunID != "run-2" || meta.PID != 42 || meta.Status != "running" {
		t.Fatalf("metadata = %+v, want port run attribution", meta)
	}
}

func TestResolveLogTargetFindsPortlessServiceLog(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "oberth.yaml"), []byte("name: demo\nservices:\n  - name: worker\n    cmd: echo worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	prev := noDaemonFlag
	noDaemonFlag = true
	t.Cleanup(func() { noDaemonFlag = prev })

	row, path, err := resolveLogTarget(context.Background(), nil, "worker", "")
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("row = %+v, want a portless service", row)
	}
	if !strings.HasSuffix(path, filepath.Join("logs", "demo", "worker.log")) {
		t.Fatalf("log path = %q, want the service log", path)
	}
}
