package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/claudehook"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// Opt-in real installed Claude, no real provider or runtime. Run only in an
// isolated environment and explicit safe-file cmd test list, never TestMain.
func TestClaudeNativeLifecycleConsumesReadonlyFinding(t *testing.T) {
	binary, native := os.Getenv("OBERTH_TEST_BINARY"), os.Getenv("OBERTH_TEST_CLAUDE")
	if binary == "" || native == "" {
		t.Skip("requires explicit candidate and Claude binary paths; no login or network provider")
	}
	base, err := os.MkdirTemp("/tmp", "oberth-native-")
	if err != nil {
		t.Fatal(err)
	}
	// Both homes are allocated together; cleanup names only this literal sandbox.
	home, state, root := filepath.Join(base, "home"), filepath.Join(base, "state"), filepath.Join(base, "project")
	for _, p := range []string{home, state, root} {
		if err = os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	defer os.RemoveAll(base)
	if out, e := exec.Command("git", "init", "-q", root).CombinedOutput(); e != nil {
		t.Fatalf("isolated git init: %s %v", out, e)
	}
	socket := filepath.Join(base, "runtime.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var conns sync.Map
	defer conns.Range(func(key, value any) bool { _ = key.(net.Conn).Close(); return true })
	at := time.Now().UTC()
	rows := []rpc.RuntimeStorageRunExit{rpc.RuntimeStorageRunExit(store.RunExitRow{ID: "selected-worker", Group: "project", Name: "worker", Code: 2, Reason: "crashed", StartedAt: at.Add(-time.Second), ExitedAt: at})}
	for i := 0; i < 8; i++ {
		rows = append(rows, rpc.RuntimeStorageRunExit(store.RunExitRow{ID: fmt.Sprint("newer-", i), Group: "project", Name: "unrelated", Reason: "completed", ExitedAt: at.Add(time.Duration(i+1) * time.Millisecond)}))
	}
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			conns.Store(conn, true)
			go func() {
				defer conn.Close()
				defer conns.Delete(conn)
				sc := bufio.NewScanner(conn)
				for sc.Scan() {
					var req rpc.Request
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						return
					}
					var result any
					switch req.Method {
					case "runtime.service.hello":
						result = map[string]any{"protocol_version": "3.0.0", "version": "3.0.0", "observation": map[string]string{"state": "observable"}}
					case "state.snapshot":
						result = map[string]any{"groups": []any{map[string]any{"name": "project", "root_dir": root, "services": []any{}, "machine": []any{}}}, "ports": []any{}, "observation": map[string]string{"state": "observable"}}
					case "runs.list":
						result = map[string]any{"runs": []any{}, "exited": []any{}}
					case "runtime.storage.read":
						result = rpc.RuntimeStorageReadResult{Section: "run_exits", RunExits: &rows}
					default:
						_ = json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "fake server only permits reads"}})
						continue
					}
					_ = json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				}
			}()
		}
	}()
	var assessments, nativeCalls, consumed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		if json.NewDecoder(r.Body).Decode(&data) != nil {
			http.Error(w, "JSON required", 400)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/decisions") {
			assessments.Add(1)
			raw, _ := json.Marshal(data)
			if bytes.Contains(raw, []byte("RAW-PROMPT-SENTINEL")) {
				t.Error("raw prompt reached assessment")
			}
			task := data["state"].(map[string]any)
			events := task["events"].([]any)
			if task["task"] != claudehook.Summary || len(events) != 1 || events[0].(map[string]any)["run_id"] != "selected-worker" {
				t.Errorf("wrong task scope: %s", raw)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "fake", "answers": map[string]any{"task_relevant": map[string]any{"type": "noul", "noul": .95}, "needs_human": map[string]any{"type": "noul", "noul": .05}, "next_option": map[string]any{"type": "choice", "choice": "inspect_logs", "probabilities": map[string]float64{"inspect_logs": 1}, "confidence": .95}}})
			return
		}
		if strings.Contains(r.URL.Path, "count_tokens") {
			_ = json.NewEncoder(w).Encode(map[string]int{"input_tokens": 100})
			return
		}
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		nativeCalls.Add(1)
		b, _ := json.Marshal(data["messages"])
		text := "INITIAL-SYNTHETIC-ANSWER"
		if bytes.Contains(b, []byte("Oberth Runtime evidence for this task")) {
			consumed.Add(1)
			text = "NATIVE-CLAUDE-CONSUMED-WORKER-FINDING"
		} else {
			time.Sleep(1200 * time.Millisecond)
			if !bytes.Contains(b, []byte("selected-worker")) {
				t.Error("Runtime facts never reached initial native task")
			}
		}
		message := map[string]any{"id": "msg_fake", "type": "message", "role": "assistant", "model": "fake", "content": []any{map[string]string{"type": "text", "text": text}}, "stop_reason": "end_turn", "stop_sequence": nil, "usage": map[string]int{"input_tokens": 100, "output_tokens": 20}}
		if stream, _ := data["stream"].(bool); !stream {
			_ = json.NewEncoder(w).Encode(message)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		message["content"] = []any{}
		message["stop_reason"] = nil
		events := []map[string]any{{"type": "message_start", "message": message}, {"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}}, {"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": text}}, {"type": "content_block_stop", "index": 0}, {"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 20}}, {"type": "message_stop"}}
		for _, e := range events {
			raw, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], raw)
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	// No user config, credentials, real network routes or host runtime socket.
	env := []string{"PATH=/opt/homebrew/bin:/usr/bin:/bin", "HOME=" + home, "BERTH_HOME=" + state, "BERTH_RUNTIME_SERVICE_NO_AUTOSTART=1", "BERTH_RUNTIME_SERVICE_SOCKET=" + socket, "BERTH_ATTN=1", "BERTH_ATTN_ENDPOINT=" + server.URL + "/decisions", "BERTH_ATTN_MODEL=fake", "BERTH_ATTN_KEY=synthetic-local-only", "ANTHROPIC_API_KEY=synthetic-local-only", "ANTHROPIC_BASE_URL=" + server.URL, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost"}
	config := exec.Command(binary, "agent", "claude-hook", "--project", root, "--run", "selected-worker", "--authorize", "inspect_logs", "--lease", "15s", "--print-config")
	config.Env = env
	config.Dir = root
	settings, err := config.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(settings, []byte(`"onFailure": "block"`)) {
		t.Fatal("task boundary must fail closed")
	}
	if err = os.Mkdir(filepath.Join(root, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".claude", "settings.json"), settings, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, native, "-p", "RAW-PROMPT-SENTINEL synthetic no-tool task", "--model", "fake", "--setting-sources", "project", "--output-format", "json", "--no-session-persistence", "--max-turns", "3", "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	cmd.Dir = root
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("NATIVE-CLAUDE-CONSUMED-WORKER-FINDING")) {
		t.Fatalf("native did not consume: %v %s", err, out)
	}
	if nativeCalls.Load() != 2 || consumed.Load() != 1 || assessments.Load() != 1 {
		t.Fatalf("unbounded or missing feedback: native=%d consumed=%d assessments=%d", nativeCalls.Load(), consumed.Load(), assessments.Load())
	}
	cursors, err := filepath.Glob(filepath.Join(state, "attention", "claude", "*", "lease.json"))
	if err != nil || len(cursors) != 1 {
		t.Fatal("missing lease")
	}
	var final claudehook.Lease
	b, _ := os.ReadFile(cursors[0])
	if json.Unmarshal(b, &final) != nil || final.Active || !final.Consumed || final.Pending != nil {
		t.Fatal("observation not reclaimed")
	}
	if c, e := net.DialTimeout("unix", socket, time.Second); e != nil {
		t.Fatal("shared Runtime was stopped")
	} else {
		c.Close()
	}
	// Exercise real self-owned observer processes too: no provider/host PID is signalled.
	for _, scenario := range []string{"SessionEnd", "StopFailure", "lease", "killed"} {
		session := "synthetic-" + scenario
		invoke := func(event string, d time.Duration) (claudehook.Response, error) {
			input, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": session, "cwd": root, "prompt": "IGNORED-RAW-PROMPT", "transcript_path": "/must-not-read", "error_details": "IGNORED-ERROR"})
			c := exec.Command(binary, "agent", "claude-hook", "--project", root, "--run", "selected-worker", "--authorize", "inspect_logs", "--lease", d.String())
			c.Env = env
			c.Dir = root
			c.Stdin = bytes.NewReader(input)
			b, e := c.Output()
			var response claudehook.Response
			if e == nil {
				e = json.Unmarshal(b, &response)
			}
			return response, e
		}
		duration := 2 * time.Second
		if scenario == "lease" {
			duration = 180 * time.Millisecond
		}
		if r, e := invoke("UserPromptSubmit", duration); e != nil || !strings.Contains(r.Message, "observing") {
			t.Fatalf("real observer not started: %+v %v", r, e)
		}
		key := claudehook.Key(groups.Canonical(root), session)
		path := filepath.Join(state, "attention", "claude", key, "lease.json")
		load := func() claudehook.Lease {
			var v claudehook.Lease
			b, _ := os.ReadFile(path)
			_ = json.Unmarshal(b, &v)
			return v
		}
		if scenario == "lease" {
			until := time.Now().Add(2 * time.Second)
			for load().Active && time.Now().Before(until) {
				time.Sleep(10 * time.Millisecond)
			}
			if load().Active {
				t.Fatal("real observer exceeded lease")
			}
		}
		if scenario == "killed" {
			pidData, e := os.ReadFile(filepath.Join(filepath.Dir(path), "active.lock"))
			if e != nil {
				t.Fatal(e)
			}
			pid, e := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if e != nil || pid <= 0 || pid == os.Getpid() {
				t.Fatal("invalid own observer PID")
			}
			own, e := os.FindProcess(pid)
			if e != nil {
				t.Fatal(e)
			}
			if e = own.Kill(); e != nil {
				t.Fatal(e)
			}
		}
		event := scenario
		if scenario == "lease" || scenario == "killed" {
			event = "Stop"
		}
		response, e := invoke(event, duration)
		if e != nil || response.Decision != "" {
			t.Fatalf("failed/canceled/expired task continued: %+v %v", response, e)
		}
		if scenario == "killed" && !strings.Contains(response.Message, "observer_lost") {
			t.Fatalf("killed observer did not fail closed: %+v", response)
		}
		if current := load(); current.Active || !current.Consumed || current.Pending != nil {
			t.Fatalf("real process retained delivery: %+v", current)
		}
	}

	// Failed task-boundary hook must not allow a new native request to consume
	// the previous generation. This is the actual 2.1.322 onFailure protocol.
	var broken map[string]any
	if err = json.Unmarshal(settings, &broken); err != nil {
		t.Fatal(err)
	}
	taskHook := broken["hooks"].(map[string]any)["UserPromptSubmit"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	taskHook["command"] = "'" + strings.ReplaceAll(binary, "'", "'\"'\"'") + "' agent claude-hook --invalid-on-purpose"
	bad, _ := json.Marshal(broken)
	if err = os.WriteFile(filepath.Join(root, ".claude", "settings.json"), bad, 0600); err != nil {
		t.Fatal(err)
	}
	before := nativeCalls.Load()
	blockCtx, blockCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer blockCancel()
	blocked := exec.CommandContext(blockCtx, native, "-p", "SYNTHETIC prompt must not enter native model", "--model", "fake", "--setting-sources", "project", "--output-format", "json", "--no-session-persistence", "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	blocked.Dir = root
	blocked.Env = env
	blockedOut, _ := blocked.CombinedOutput()
	if blockCtx.Err() != nil || nativeCalls.Load() != before {
		t.Fatalf("failed boundary did not block prompt: calls=%d->%d %s", before, nativeCalls.Load(), blockedOut)
	}

	t.Log("real Claude consumed one scoped read-only finding through Stop JSON; visible native answer returned; observer reclaimed; shared fake Runtime retained")
}
