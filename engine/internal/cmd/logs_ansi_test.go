package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
)

// coloredLog is a service log whose own output carries the SGR colour codes a
// dev server writes even when its stdout is a file.
const coloredLog = "\x1b[31mERROR\x1b[0m could not bind\n\x1b[32mok\x1b[0m\n"

func withNoColor(t *testing.T, off bool) {
	t.Helper()
	previous := display.NoColor
	display.NoColor = off
	t.Cleanup(func() { display.NoColor = previous })
}

func writeColoredLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(path, []byte(coloredLog), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The no-color rule reaches the service's own lines: a reader that asked for
// no colour gets text, while the file keeps what the service wrote.
func TestReadFileLogsStripsSGRWhenColorIsOff(t *testing.T) {
	path := writeColoredLog(t)
	withNoColor(t, true)

	doc, err := readFileLogs(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ERROR could not bind", "ok"}
	if len(doc.Lines) != len(want) || doc.Lines[0] != want[0] || doc.Lines[1] != want[1] {
		t.Fatalf("lines = %q, want %q", doc.Lines, want)
	}
}

func TestReadFileLogsKeepsSGRWhenColorIsOn(t *testing.T) {
	path := writeColoredLog(t)
	withNoColor(t, false)

	doc, err := readFileLogs(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Lines) == 0 || !strings.Contains(doc.Lines[0], "\x1b[31m") {
		t.Fatalf("lines = %q, want the service's own bytes", doc.Lines)
	}
}

func TestReadFileLogsFromStripsSGRWhenColorIsOff(t *testing.T) {
	path := writeColoredLog(t)
	withNoColor(t, true)

	doc, err := readFileLogsFrom(path, logRange{start: 0, end: -1}, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ERROR could not bind", "ok"}
	if len(doc.Lines) != len(want) || doc.Lines[0] != want[0] || doc.Lines[1] != want[1] {
		t.Fatalf("lines = %q, want %q", doc.Lines, want)
	}
}

func TestLogLineSinkStripsSGRWhenColorIsOff(t *testing.T) {
	withNoColor(t, true)
	sink := logLineSink("api.log", false, logMetadata{})
	out := captureStdout(t, func() {
		if err := sink("\x1b[31mboom\x1b[0m"); err != nil {
			t.Error(err)
		}
	})
	if strings.Contains(out, "\x1b") || !strings.Contains(out, "boom") {
		t.Fatalf("output = %q, want plain text", out)
	}
}

func TestFirstErrorTextStripsSGRWhenColorIsOff(t *testing.T) {
	path := writeColoredLog(t)
	withNoColor(t, true)
	previousJSON := logsJSONFlag
	logsJSONFlag = true
	t.Cleanup(func() { logsJSONFlag = previousJSON })

	var err error
	out := captureStdout(t, func() {
		err = logsFirstErrorFromFile(path, logRange{start: 0, end: -1}, logMetadata{})
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc logsDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("decoding the document: %v\n%s", err, out)
	}
	if doc.FirstError == nil {
		t.Fatalf("no first error in %s", out)
	}
	if strings.Contains(doc.FirstError.Text, "\x1b") || !strings.Contains(doc.FirstError.Text, "ERROR") {
		t.Fatalf("first error text = %q, want plain text", doc.FirstError.Text)
	}
}
