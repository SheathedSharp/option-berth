package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runs"
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

// TestSelectBoundPortChoosesOneRow pins the lookup's three answers: the
// matching row, the documented not_found, and ambiguous with the --ip remedy.
func TestSelectBoundPortChoosesOneRow(t *testing.T) {
	rows := []ports.ListeningPort{
		{Port: 3000, BindAddress: "127.0.0.1"},
		{Port: 3000, BindAddress: "0.0.0.0"},
		{Port: 5432, BindAddress: "0.0.0.0"},
	}

	row, err := selectBoundPort(rows, 3000, "127.0.0.1")
	if err != nil || row == nil || row.BindAddress != "127.0.0.1" {
		t.Fatalf("row = %+v err = %v, want the bound listener", row, err)
	}

	_, err = selectBoundPort(rows, 3000, "")
	if code, _, _ := describe(err); code != "ambiguous" {
		t.Fatalf("code = %q, want ambiguous for a multi-bind port", code)
	}

	_, err = selectBoundPort(rows, 9999, "")
	if code, msg, _ := describe(err); code != "not_found" || !strings.Contains(msg, "9999") {
		t.Fatalf("code = %q msg = %q, want not_found naming the port", code, msg)
	}

	_, err = selectBoundPort(rows, 3000, "::1")
	if code, msg, _ := describe(err); code != "not_found" || !strings.Contains(msg, "::1:3000") {
		t.Fatalf("code = %q msg = %q, want not_found naming the bind address", code, msg)
	}
}

// TestLogSelectorCarriesPortAndOnlyANonEmptyBind: the wire selector names the
// port always and the bind address only when there is one.
func TestLogSelectorCarriesPortAndOnlyANonEmptyBind(t *testing.T) {
	sel := logSelector(ports.ListeningPort{Port: 3000, BindAddress: "127.0.0.1"})
	if sel.Port == nil || *sel.Port != 3000 || sel.BindAddress == nil || *sel.BindAddress != "127.0.0.1" {
		t.Fatalf("selector = %+v, want port and bind", sel)
	}
	sel = logSelector(ports.ListeningPort{Port: 5432})
	if sel.Port == nil || *sel.Port != 5432 || sel.BindAddress != nil {
		t.Fatalf("selector = %+v, want no bind field", sel)
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
