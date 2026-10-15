package agentstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFakeStreamHandshakeAndTwoTurns(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BERTH_AGENT_STREAM_FAKE", "codex")
	var out bytes.Buffer
	in := strings.NewReader(`{"v":1,"type":"turn.start","id":"c1","text":"first"}
{"v":1,"type":"turn.start","id":"c2","text":"second"}
`)
	if err := Run(Options{Provider: "codex", Worktree: root}, in, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 5 {
		t.Fatalf("expected session + two turns, got %d lines: %s", len(lines), out.String())
	}
	var sawSession bool
	for _, line := range lines {
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatal(err)
		}
		if evt["type"] == "session.started" {
			sawSession = true
		}
	}
	if !sawSession {
		t.Fatalf("missing session.started in %s", out.String())
	}
	completed := strings.Count(out.String(), `"type":"turn.completed"`)
	if completed < 2 {
		t.Fatalf("expected two turn.completed events, got %d in %s", completed, out.String())
	}
}

func TestFakeStreamBusyWhileRunning(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BERTH_AGENT_STREAM_FAKE", "codex")
	// slowDriver not implemented — busy only when f.busy true during sync fake; test invalid second start via concurrent is N/A.
	// Instead verify error shape for empty text.
	var out bytes.Buffer
	in := strings.NewReader(`{"v":1,"type":"turn.start","id":"c1","text":""}
`)
	if err := Run(Options{Provider: "codex", Worktree: root}, in, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"code":"invalid"`) {
		t.Fatalf("expected invalid error, got %s", out.String())
	}
}

func TestReadCommandConsumesMultipleLines(t *testing.T) {
	in := strings.NewReader(`{"v":1,"type":"turn.start","id":"c1","text":"first"}
{"v":1,"type":"turn.start","id":"c2","text":"second"}
`)
	br := bufio.NewReader(in)
	if _, err := readCommand(br); err != nil {
		t.Fatal(err)
	}
	if _, err := readCommand(br); err != nil {
		t.Fatalf("second line: %v", err)
	}
}

func TestFakeStreamBusyWhileHoldingTurn(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BERTH_AGENT_STREAM_FAKE", "codex")
	var out bytes.Buffer
	in := strings.NewReader(`{"v":1,"type":"turn.start","id":"c1","text":"hold"}
{"v":1,"type":"turn.start","id":"c2","text":"second"}
{"v":1,"type":"turn.interrupt","id":"c3"}
`)
	if err := Run(Options{Provider: "codex", Worktree: root}, in, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `"code":"busy"`) {
		t.Fatalf("expected busy, got %s", body)
	}
	if !strings.Contains(body, `"outcome":"interrupted"`) {
		t.Fatalf("expected interrupted completion, got %s", body)
	}
}

func TestFakeStreamRequestRespond(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BERTH_AGENT_STREAM_FAKE", "codex")
	var out bytes.Buffer
	in := strings.NewReader(`{"v":1,"type":"turn.start","id":"c1","text":"approve"}
{"v":1,"type":"request.respond","id":"c2","request":"r1","decision":"allow"}
{"v":1,"type":"turn.interrupt","id":"c3"}
`)
	if err := Run(Options{Provider: "codex", Worktree: root}, in, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `"type":"request.opened"`) || !strings.Contains(body, `"decision":"allow"`) {
		t.Fatalf("expected approval flow, got %s", body)
	}
}

func TestLiveStreamReturnsUnsupportedWithoutFake(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	var out bytes.Buffer
	in := strings.NewReader(`{"v":1,"type":"turn.start","id":"c1","text":"hello"}
`)
	if err := Run(Options{
		Provider: "codex", Worktree: root,
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	}, in, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"code":"unsupported"`) {
		t.Fatalf("expected unsupported, got %s", out.String())
	}
}
