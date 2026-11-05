package sessions

import (
	"strings"
	"testing"
)

// lookup builds a Getenv over a literal table.
func lookup(table map[string]string) func(string) string {
	return func(key string) string { return table[key] }
}

// agentChain is a three-process ancestry used by the detection tests:
//
//	300  the oberth command doing the detecting
//	200  the shell around it
//	100  the Claude Code process at the top
func agentChain() []Process {
	return []Process{
		{PID: 300, PPID: 200, Command: "oberth"},
		{PID: 200, PPID: 100, Command: "/bin/zsh"},
		{PID: 100, PPID: 1, Command: "/opt/homebrew/bin/claude"},
	}
}

func detectWith(t *testing.T, table map[string]string) (id, tool, label string, detected, found bool) {
	t.Helper()
	session, ok := Detect(Options{Getenv: lookup(table), PID: 300, Processes: agentChain})
	return session.ID, session.Tool, session.Label, session.Detected, ok
}

func TestNothingIdentifiesANSessionWithoutEvidence(t *testing.T) {
	if _, _, _, _, found := detectWith(t, nil); found {
		t.Fatal("an empty environment produced a session")
	}
	if _, _, _, _, found := detectWith(t, map[string]string{EnvClaudeCode: "0"}); found {
		t.Fatal(`CLAUDECODE=0 was treated as a present agent`)
	}
	for _, off := range []string{"false", "no", "off", " FALSE "} {
		if _, _, _, _, found := detectWith(t, map[string]string{EnvCursorAgent: off}); found {
			t.Errorf("%q was treated as a present agent", off)
		}
	}
}

func TestDeclaredSessionIsTakenVerbatim(t *testing.T) {
	cases := []struct {
		name     string
		declared string
		id       string
		tool     string
	}{
		{"tool and id", "claude-code:abc123", "abc123", "claude-code"},
		{"another tool", "codex:t-9", "t-9", "codex"},
		{"id only", "just-an-id", "just-an-id", ToolAgent},
		{"empty id after separator", "claude-code:", "claude-code:", ToolAgent},
		{"padding is trimmed", "  cursor : c-1  ", "c-1", "cursor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, tool, _, detected, found := detectWith(t, map[string]string{EnvSession: tc.declared})
			if !found || id != tc.id || tool != tc.tool {
				t.Fatalf("got id=%q tool=%q found=%v, want id=%q tool=%q", id, tool, found, tc.id, tc.tool)
			}
			if detected {
				t.Error("a declared session was marked detected")
			}
		})
	}
}

func TestDeclarationBeatsEveryMarker(t *testing.T) {
	id, tool, _, detected, found := detectWith(t, map[string]string{
		EnvSession:       "codex:t-old",
		EnvClaudeCode:    "1",
		EnvCodexThreadID: "t-new",
		EnvCursorAgent:   "1",
	})
	if !found || id != "t-old" || tool != ToolCodex || detected {
		t.Fatalf("declaration lost: id=%q tool=%q detected=%v", id, tool, detected)
	}
	if _, _, label, _, _ := detectWith(t, map[string]string{
		EnvSession: "codex:t-old", EnvSessionLabel: "night shift",
	}); label != "night shift" {
		t.Errorf("label = %q", label)
	}
}

func TestSplitSessionIDAndTool(t *testing.T) {
	_, tool, _, detected, found := detectWith(t, map[string]string{EnvSessionID: "id-7"})
	if !found || tool != ToolAgent || detected {
		t.Fatalf("bare id gave tool=%q detected=%v found=%v", tool, detected, found)
	}
	_, tool, _, _, found = detectWith(t, map[string]string{EnvSessionID: "id-7", EnvSessionTool: "aider"})
	if !found || tool != "aider" {
		t.Fatalf("explicit tool gave %q", tool)
	}
}

func TestClaudeCodeRecognition(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		id   string
	}{
		{"marker only, id from the agent process", map[string]string{EnvClaudeCode: "1"}, "pid-100"},
		{"session id variable", map[string]string{EnvClaudeCode: "1", EnvClaudeCodeSessionID: "cc-42"}, "cc-42"},
		{"legacy session id alone", map[string]string{EnvClaudeSessionID: "cc-legacy"}, "cc-legacy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, tool, _, detected, found := detectWith(t, tc.env)
			if !found || tool != ToolClaudeCode || id != tc.id || !detected {
				t.Fatalf("got id=%q tool=%q detected=%v found=%v", id, tool, detected, found)
			}
		})
	}
}

func TestCodexAndCursorRecognition(t *testing.T) {
	id, tool, _, detected, found := detectWith(t, map[string]string{EnvCodexThreadID: "thr-1"})
	if !found || tool != ToolCodex || id != "thr-1" || !detected {
		t.Fatalf("codex thread = %q/%q/%v/%v", id, tool, detected, found)
	}

	// The sandbox marker names no id, and no codex process is in the chain,
	// so the immediate parent supplies the id.
	id, tool, _, detected, found = detectWith(t, map[string]string{EnvCodexSandbox: "seatbelt"})
	if !found || tool != ToolCodex || id != "pid-200" || !detected {
		t.Fatalf("codex sandbox = %q/%q/%v/%v", id, tool, detected, found)
	}

	id, tool, _, detected, found = detectWith(t, map[string]string{
		EnvCursorAgent: "1", EnvCursorAgentSessionID: "cur-3",
	})
	if !found || tool != ToolCursor || id != "cur-3" || !detected {
		t.Fatalf("cursor = %q/%q/%v/%v", id, tool, detected, found)
	}
}

func TestClaudeCodeOutranksCodex(t *testing.T) {
	_, tool, _, _, found := detectWith(t, map[string]string{
		EnvClaudeCodeSessionID: "cc-1",
		EnvCodexThreadID:       "thr-1",
	})
	if !found || tool != ToolClaudeCode {
		t.Fatalf("tool = %q, want claude-code", tool)
	}
}

func TestAncestryIDSurvivesAcrossCommands(t *testing.T) {
	first, _ := Detect(Options{
		Getenv: lookup(map[string]string{EnvClaudeCode: "1"}), PID: 300, Processes: agentChain,
	})
	second, _ := Detect(Options{
		Getenv: lookup(map[string]string{EnvClaudeCode: "1"}), PID: 200, Processes: agentChain,
	})
	if first.ID != "pid-100" || second.ID != first.ID {
		t.Errorf("ids %q and %q, want both pid-100", first.ID, second.ID)
	}
}

func TestAncestryFallsBackToTheParentWhenTheAgentIsGone(t *testing.T) {
	table := []Process{
		{PID: 300, PPID: 200, Command: "oberth"},
		{PID: 200, PPID: 1, Command: "zsh"},
	}
	got, ok := Detect(Options{
		Getenv: lookup(map[string]string{EnvClaudeCode: "1"}),
		PID:    300,
		Processes: func() []Process {
			return table
		},
	})
	if !ok || got.ID != "pid-200" {
		t.Fatalf("id = %q, want the parent pid-200", got.ID)
	}
}

func TestProcessNameMatching(t *testing.T) {
	claude := detector{tool: ToolClaudeCode, binaries: []string{"claude"}}
	for _, command := range []string{"claude", "/usr/local/bin/claude", `C:\tools\Claude.EXE`, "  CLAUDE  "} {
		if !claude.isAgent(command) {
			t.Errorf("%q was not recognised", command)
		}
	}
	for _, command := range []string{"", "claude-code", "/bin/zsh", "myclaude", "claude --resume"} {
		if claude.isAgent(command) {
			t.Errorf("%q was wrongly recognised", command)
		}
	}
}

func TestDetectFromEnvSlice(t *testing.T) {
	got, found := DetectFromEnv([]string{"PATH=/bin", "BERTH_SESSION=cursor:c-1", "IGNORED"}, Options{})
	if !found || got.ID != "c-1" || got.Tool != ToolCursor || got.Detected {
		t.Fatalf("DetectFromEnv = %+v, %v", got, found)
	}
	if _, found := DetectFromEnv([]string{"PATH=/bin"}, Options{Getenv: lookup(map[string]string{EnvClaudeCode: "1"})}); found {
		t.Fatal("the slice table did not replace the previous Getenv")
	}
}

func TestBuildSessionDefaultsToThisProcess(t *testing.T) {
	got := buildSession(ToolAgent, "", "", false)
	if !strings.HasPrefix(got.ID, "pid-") {
		t.Errorf("id = %q, want a pid token", got.ID)
	}
}
