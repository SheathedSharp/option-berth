// Package sessions answers one question before a command starts: which coding
// agent, in which checkout, asked for this run? The answer is attached to the
// run and inherited by every port the run opens, so runtime facts can be read
// back per agent instead of per anonymous process.
//
// Declaration beats recognition. BERTH_SESSION states the session outright and
// is never questioned. BERTH_SESSION_ID and BERTH_SESSION_TOOL carry the same
// information in two pieces. Only when neither is present does the package
// read the environment a known agent leaves behind, and a session found that
// way is marked Detected: it is a probable attribution, not a claim.
//
// The check runs once, in the process about to spawn the command. Later
// readers consume the recorded session rather than detecting it again.
package sessions

import (
	"os"
	"strconv"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// Environment keys that declare a session. The first four are the contract;
// the rest are the markers the supported agents export themselves.
const (
	EnvSession      = "BERTH_SESSION"       // "<tool>:<id>", the explicit contract
	EnvSessionID    = "BERTH_SESSION_ID"    // an id on its own
	EnvSessionTool  = "BERTH_SESSION_TOOL"  // the tool for EnvSessionID
	EnvSessionLabel = "BERTH_SESSION_LABEL" // human label, any form

	// Claude Code
	EnvClaudeCode          = "CLAUDECODE"
	EnvClaudeCodeSessionID = "CLAUDE_CODE_SESSION_ID"
	EnvClaudeSessionID     = "CLAUDE_SESSION_ID"
	// Codex
	EnvCodexSandbox  = "CODEX_SANDBOX"
	EnvCodexThreadID = "CODEX_THREAD_ID"
	// Cursor
	EnvCursorAgent          = "CURSOR_AGENT"
	EnvCursorAgentSessionID = "CURSOR_AGENT_SESSION_ID"
)

// Tool names published in Session.tool. Clients switch on these strings to
// choose an icon, so they are part of the stable interface.
const (
	ToolClaudeCode = "claude-code"
	ToolCodex      = "codex"
	ToolCursor     = "cursor"
	// ToolAgent covers a caller that supplied an id but named no tool.
	ToolAgent = "agent"
)

// Process is one row of the process table the ancestry walk reads.
type Process struct {
	PID     int
	PPID    int
	Command string
}

// Options supplies Detect's inputs. The zero value is the production path:
// the real environment, the current pid and the platform's process table.
type Options struct {
	// Getenv reads one variable. Nil means os.Getenv.
	Getenv func(string) string
	// PID is where the ancestry walk starts. Zero means os.Getpid.
	PID int
	// Processes returns the process table. Nil means the platform's own; an
	// empty table leaves only the parent pid as a fallback id.
	Processes func() []Process
}

// ancestorLimit stops a malformed or cyclic process table from looping.
const ancestorLimit = 64

// environment is the variable lookup Detect reads through, so the same logic
// serves os.Getenv and a KEY=VALUE slice.
type environment struct {
	get func(string) string
}

func newEnvironment(get func(string) string) environment {
	if get == nil {
		get = os.Getenv
	}
	return environment{get: get}
}

// text returns the trimmed value, so padding never reaches the wire.
func (e environment) text(key string) string {
	return strings.TrimSpace(e.get(key))
}

// set reports whether a variable is present and not one of the shell's ways of
// saying "off": a hand-written script may export CLAUDECODE=0.
func (e environment) set(key string) bool {
	switch strings.ToLower(strings.TrimSpace(e.get(key))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// detector is one agent family: the variables that reveal it, the variables
// that carry its own id, and the process names its binary uses.
type detector struct {
	tool     string
	signals  []string
	idKeys   []string
	binaries []string
}

// detectorTable is the recognition order. Claude Code comes first because its
// marker is the one most likely to coexist with another tool's in a nested
// terminal.
var detectorTable = []detector{
	{
		tool:     ToolClaudeCode,
		signals:  []string{EnvClaudeCode, EnvClaudeCodeSessionID, EnvClaudeSessionID},
		idKeys:   []string{EnvClaudeCodeSessionID, EnvClaudeSessionID},
		binaries: []string{"claude"},
	},
	{
		tool:     ToolCodex,
		signals:  []string{EnvCodexThreadID, EnvCodexSandbox},
		idKeys:   []string{EnvCodexThreadID},
		binaries: []string{"codex"},
	},
	{
		tool:     ToolCursor,
		signals:  []string{EnvCursorAgent, EnvCursorAgentSessionID},
		idKeys:   []string{EnvCursorAgentSessionID},
		binaries: []string{"cursor-agent", "cursor"},
	},
}

// observe reports whether the agent is present and, when it is, the id the
// agent named for itself (empty when it named none).
func (d detector) observe(env environment) (string, bool) {
	present := false
	for _, key := range d.signals {
		if env.set(key) {
			present = true
			break
		}
	}
	if !present {
		return "", false
	}
	for _, key := range d.idKeys {
		if id := env.text(key); id != "" {
			return id, true
		}
	}
	return "", true
}

// isAgent reports whether a process command names this agent's binary. A full
// path and the Windows .exe suffix are tolerated; a substring is not.
func (d detector) isAgent(command string) bool {
	name := strings.ToLower(strings.TrimSpace(command))
	if name == "" {
		return false
	}
	if slash := strings.LastIndexAny(name, `/\`); slash >= 0 {
		name = name[slash+1:]
	}
	name = strings.TrimSuffix(name, ".exe")
	for _, binary := range d.binaries {
		if name == binary {
			return true
		}
	}
	return false
}

// Detect resolves the session that launched this process.
//
// The boolean is false when nothing identifies an agent. Inventing a session
// for a plain shell would attribute every dev server on the machine to a
// session that never existed, so absence stays absence.
func Detect(opts Options) (state.Session, bool) {
	env := newEnvironment(opts.Getenv)
	label := env.text(EnvSessionLabel)

	if declared := env.text(EnvSession); declared != "" {
		return declaredSession(declared, label), true
	}

	if id := env.text(EnvSessionID); id != "" {
		tool := env.text(EnvSessionTool)
		if tool == "" {
			tool = ToolAgent
		}
		return buildSession(tool, id, label, false), true
	}

	for _, d := range detectorTable {
		id, present := d.observe(env)
		if !present {
			continue
		}
		if id == "" {
			id = ancestryID(opts, d)
		}
		return buildSession(d.tool, id, label, true), true
	}

	return state.Session{}, false
}

// declaredSession parses BERTH_SESSION. A value without a usable tool prefix
// is an id that happens to contain a colon, not a tool name.
func declaredSession(value, label string) state.Session {
	tool, id, separated := strings.Cut(value, ":")
	if !separated || strings.TrimSpace(id) == "" {
		return buildSession(ToolAgent, value, label, false)
	}
	return buildSession(strings.TrimSpace(tool), strings.TrimSpace(id), label, false)
}

// DetectFromEnv runs Detect over a KEY=VALUE slice: the shape an environment
// has on the wire (`runs.spawn {env}`) and inside exec.Cmd.
func DetectFromEnv(env []string, opts Options) (state.Session, bool) {
	values := make(map[string]string, len(env))
	for _, pair := range env {
		key, value, separated := strings.Cut(pair, "=")
		if separated {
			values[key] = value
		}
	}
	opts.Getenv = func(key string) string { return values[key] }
	return Detect(opts)
}

// ancestryID supplies the id for an agent that names none: the pid of the
// nearest ancestor running the agent binary. That pid lasts as long as the
// agent, which is the lifetime cleanup actually cares about.
func ancestryID(opts Options, d detector) string {
	start := opts.PID
	if start == 0 {
		start = os.Getpid()
	}
	list := opts.Processes
	if list == nil {
		list = processTable
	}

	if table := list(); len(table) > 0 {
		index := make(map[int]Process, len(table))
		for _, row := range table {
			index[row.PID] = row
		}
		current := start
		for hop := 0; hop < ancestorLimit; hop++ {
			row, known := index[current]
			if !known {
				break
			}
			if d.isAgent(row.Command) {
				return pidToken(row.PID)
			}
			if row.PPID <= 1 || row.PPID == current {
				break
			}
			current = row.PPID
		}
		// The marker is set but the agent is not in our ancestry: a hook
		// exported it into a shell the agent has already detached from. The
		// immediate parent outlives this short-lived command, so it is the
		// better id of the two.
		if self, known := index[start]; known && self.PPID > 1 {
			return pidToken(self.PPID)
		}
	}

	if parent := os.Getppid(); parent > 1 {
		return pidToken(parent)
	}
	return pidToken(start)
}

// buildSession produces the wire object; the git context is Capture's job.
func buildSession(tool, id, label string, detected bool) state.Session {
	if id == "" {
		id = pidToken(os.Getpid())
	}
	session := state.Session{ID: id, Tool: tool, Label: label, Detected: detected}
	return session
}

func pidToken(pid int) string {
	return "pid-" + strconv.Itoa(pid)
}
