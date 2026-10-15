package agentstream

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func buildFakeProvider(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src := filepath.Join(filepath.Dir(file), "testdata", "fake"+name)
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake codex: %v\n%s", err, out)
	}
	return bin
}

func TestCodexLiveFakeBinaryTwoTurns(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	bin := buildFakeProvider(t, "codex")
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
		Provider: "codex", Worktree: root,
		LookPath: func(name string) (string, error) {
			if name == "codex" {
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

func TestCodexLiveFakeBinaryBusyAndApproval(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	bin := buildFakeProvider(t, "codex")
	lookPath := func(name string) (string, error) {
		if name == "codex" {
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
		_ = inW.Close()
	}()
	if err := Run(Options{Provider: "codex", Worktree: root, LookPath: lookPath}, inR, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"code":"busy"`) {
		t.Fatalf("expected busy: %s", out.String())
	}

	out.Reset()
	inR, inW = io.Pipe()
	go func() {
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c1","text":"approve"}`+"\n")
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(inW, `{"v":1,"type":"request.respond","id":"c2","request":"r1","decision":"allow"}`+"\n")
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.interrupt","id":"c3"}`+"\n")
		_ = inW.Close()
	}()
	if err := Run(Options{Provider: "codex", Worktree: root, LookPath: lookPath}, inR, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `"type":"request.opened"`) || !strings.Contains(body, `"decision":"allow"`) {
		t.Fatalf("expected approval flow: %s", body)
	}
}

func TestCodexLiveCloseInterruptsHold(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	bin := buildFakeProvider(t, "codex")
	inR, inW := io.Pipe()
	go func() {
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c1","text":"hold"}`+"\n")
		time.Sleep(400 * time.Millisecond)
		_ = inW.Close()
	}()
	var out bytes.Buffer
	if err := Run(Options{
		Provider: "codex", Worktree: root,
		LookPath: func(name string) (string, error) {
			if name == "codex" {
				return bin, nil
			}
			return "", exec.ErrNotFound
		},
	}, inR, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `"outcome":"interrupted"`) && !strings.Contains(body, `"outcome":"completed"`) {
		t.Fatalf("expected turn to end on close: %s", body)
	}
	if !strings.Contains(body, `"type":"session.ended"`) {
		t.Fatalf("expected session.ended: %s", body)
	}
}

func TestCodexLiveRejectsUnknownProviderRequest(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	bin := buildFakeProvider(t, "codex")
	inR, inW := io.Pipe()
	go func() {
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c1","text":"unknown"}`+"\n")
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.interrupt","id":"c2"}`+"\n")
		_ = inW.Close()
	}()
	var out bytes.Buffer
	if err := Run(Options{
		Provider: "codex", Worktree: root,
		LookPath: func(name string) (string, error) {
			if name == "codex" {
				return bin, nil
			}
			return "", exec.ErrNotFound
		},
	}, inR, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"code":"unsupported_request"`) {
		t.Fatalf("expected unsupported_request notice: %s", out.String())
	}
}

func TestCodexLiveMapsItemDelta(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	bin := buildFakeProvider(t, "codex")
	inR, inW := io.Pipe()
	go func() {
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c1","text":"items"}`+"\n")
		time.Sleep(600 * time.Millisecond)
		_ = inW.Close()
	}()
	var out bytes.Buffer
	if err := Run(Options{
		Provider: "codex", Worktree: root,
		LookPath: func(name string) (string, error) {
			if name == "codex" {
				return bin, nil
			}
			return "", exec.ErrNotFound
		},
	}, inR, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"type":"item.updated"`) {
		t.Fatalf("expected item.updated: %s", out.String())
	}
}

func TestTurnTextNotInProviderArgv(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	fakeBin := buildFakeProvider(t, "codex")
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	wrapper := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$0\" \"$1\" \"$2\" >> \"" + argvLog + "\"\nexec \"" + fakeBin + "\" \"$1\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	const sentinel = "SENTINEL_TURN_TEXT_DO_NOT_LEAK"
	inR, inW := io.Pipe()
	go func() {
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c1","text":"`+sentinel+`"}`+"\n")
		time.Sleep(600 * time.Millisecond)
		_ = inW.Close()
	}()
	if err := Run(Options{
		Provider: "codex", Worktree: root,
		LookPath: func(name string) (string, error) {
			if name == "codex" {
				return wrapper, nil
			}
			return "", exec.ErrNotFound
		},
	}, inR, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), sentinel) {
		t.Fatalf("turn text leaked into argv log: %s", logged)
	}
}

func TestClaudeLiveFakeBinaryTurn(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	bin := buildFakeProvider(t, "claude")
	inR, inW := io.Pipe()
	go func() {
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c1","text":"hello"}`+"\n")
		time.Sleep(400 * time.Millisecond)
		_ = inW.Close()
	}()
	var out bytes.Buffer
	if err := Run(Options{
		Provider: "claude", Worktree: root,
		LookPath: func(name string) (string, error) {
			if name == "claude" {
				return bin, nil
			}
			return "", exec.ErrNotFound
		},
	}, inR, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `"type":"session.started"`) || !strings.Contains(body, `"provider":"claude"`) {
		t.Fatalf("missing claude session: %s", body)
	}
	if !strings.Contains(body, `"type":"turn.completed"`) {
		t.Fatalf("missing completion: %s", body)
	}
}

func TestClaudeLiveFakeAskUserNotice(t *testing.T) {
	root := t.TempDir()
	os.Unsetenv("BERTH_AGENT_STREAM_FAKE")
	bin := buildFakeProvider(t, "claude")
	inR, inW := io.Pipe()
	go func() {
		_, _ = io.WriteString(inW, `{"v":1,"type":"turn.start","id":"c1","text":"ask-me"}`+"\n")
		time.Sleep(400 * time.Millisecond)
		_ = inW.Close()
	}()
	var out bytes.Buffer
	if err := Run(Options{
		Provider: "claude", Worktree: root,
		LookPath: func(name string) (string, error) {
			if name == "claude" {
				return bin, nil
			}
			return "", exec.ErrNotFound
		},
	}, inR, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `"code":"provider_question"`) {
		t.Fatalf("missing provider_question notice: %s", body)
	}
}
