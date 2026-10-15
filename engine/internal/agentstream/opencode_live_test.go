package agentstream

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeLiveFakeBinaryTwoTurns(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	bin := buildFakeProvider(t, "opencode")
	var out bytes.Buffer
	inR, inW := io.Pipe()
	go func() {
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c1","text":"first"}`+"\n")
		time.Sleep(1 * time.Second)
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c2","text":"second"}`+"\n")
		time.Sleep(800 * time.Millisecond)
		_ = inW.Close()
	}()
	if err := Run(Options{
		Provider: "opencode", Worktree: root,
		LookPath: func(name string) (string, error) {
			if name == "opencode" {
				return bin, nil
			}
			return "", exec.ErrNotFound
		},
	}, inR, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `"type":"session.started"`) {
		t.Fatalf("missing session.started: %s", body)
	}
	if strings.Count(body, `"type":"turn.completed"`) < 2 {
		t.Fatalf("expected two completions: %s", body)
	}
}

func TestOpenCodeLiveFakeBinaryBusyAndApproval(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	bin := buildFakeProvider(t, "opencode")
	lookPath := func(name string) (string, error) {
		if name == "opencode" {
			return bin, nil
		}
		return "", exec.ErrNotFound
	}
	var out bytes.Buffer
	inR, inW := io.Pipe()
	go func() {
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c1","text":"hold"}`+"\n")
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c2","text":"second"}`+"\n")
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.interrupt","id":"c3"}`+"\n")
		time.Sleep(500 * time.Millisecond)
		_ = inW.Close()
	}()
	if err := Run(Options{
		Provider: "opencode", Worktree: root, LookPath: lookPath,
	}, inR, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `"code":"busy"`) {
		t.Fatalf("missing busy: %s", body)
	}
	if !strings.Contains(body, `"outcome":"interrupted"`) {
		t.Fatalf("missing interrupted: %s", body)
	}
}
