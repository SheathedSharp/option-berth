// Package draft runs the user's own local coding agent to draft a
// oberth.yaml — the answer to "what should this project's services be",
// which option-berth deliberately does not guess (docs/product.md: declarations,
// evidence, and judgments are three different things).
//
// The agent is a CLI the user already has and trusts. option-berth hands it the
// facts it gathered itself (listeners, the project root, any existing
// manifest) plus a set of drafting rules, and what comes back is a **draft**:
// it lives under ~/.option-berth/drafts until a person adopts it with
// `oberth init adopt`. Nothing in this package writes a manifest.
package draft

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/spawn"
)

// CLI is one supported local agent: the program to find, and the arguments
// that make it run one prompt headlessly and print its answer on stdout.
//
// The flags below were pinned against the installed CLIs on this machine
// (2026-09-23) rather than from documentation, because that is the version
// that will run. If a flag stops working, that is what Pinned is for.
//
	//	claude 2.1.282    `-p/--print` is the non-interactive switch ("starts an
//	                  interactive session by default, use -p/--print for
//	                  non-interactive output"); the output format defaults to
//	                  text. Nothing about permissions is passed: read-only work
//	                  runs headless, anything needing approval is denied rather
//	                  than bypassed, and agent.args is where a broader posture
//	                  goes (e.g. ["--allowedTools", "Bash"]).
//	codex-cli 0.154.0 `exec` is the non-interactive mode. --skip-git-repo-check
//	                  because a manifest may be drafted outside a repository —
//	                  plain `oberth init` supports that, so the drafting run
//	                  has to as well. The sandbox and approval defaults come
//	                  from the user's own Codex config; none is overridden here.
type CLI struct {
	// Name is the value accepted by `oberth init draft [agent]`: claude, codex.
	Name string
	// Bin is the program looked up on PATH.
	Bin string
	// Flags precede the prompt on the command line.
	Flags []string
	// ModelArgs is the flag that takes a model name on this CLI, when the
	// caller asks for one: claude spells it `--model`, codex `-m`.
	ModelArgs []string
	// Structured is how this CLI is asked for a schema-constrained answer.
	// Every supported CLI has one — it is what makes a draft a document rather
	// than a fenced block to be parsed back out of prose.
	Structured *Structured
	// Pinned records what was verified, and when — it is printed with the
	// draft's provenance so a stale pin is visible.
	Pinned string
}

// Structured is how one CLI is asked for an answer conforming to a JSON Schema,
// and where that answer is read from afterwards.
//
// The two CLIs differ in both places, and neither difference is avoidable by
// choosing one convention: claude takes the schema inline and answers with an
// envelope on stdout (`structured_output` inside a result object), while codex
// takes a path and writes the final message to a file of its own — the copy on
// its stdout is a stream of events, not the answer.
type Structured struct {
	// Args are the extra flags for a structured run. schema is the schema text
	// (for a CLI that takes it inline), schemaPath and answerPath are files in
	// a temporary directory the caller owns: it wrote the schema there before
	// the run and reads the answer after it.
	Args func(schema, schemaPath, answerPath string) []string
	// Answer pulls the answer document out of a finished run.
	Answer func(stdout, answerPath string) ([]byte, error)
}

// clis is the supported matrix, in auto-detection order: the first one that
// resolves on PATH wins when init draft is given without an agent and the config
// names none.
//
// The probes were pinned on the same day as the run flags: claude's
// `auth status` prints one JSON object (loggedIn / authMethod) rather than a
// sentence, which is what LoginSummary is for, while codex's `login status`
// prints "Logged in using ChatGPT".
var clis = []CLI{
	{Name: "claude", Bin: "claude", Flags: []string{"-p"}, ModelArgs: []string{"--model"},
		Structured: &Structured{
			// stream-json keeps the final `structured_output` result that
			// claudeAnswer reads, while giving the desktop client lifecycle and
			// tool-use frames to show as progress. `--verbose` is required by
			// Claude when stream-json is used with --print.
			Args: func(schema, _, _ string) []string {
				return []string{"--output-format", "stream-json", "--verbose", "--json-schema", schema}
			},
			Answer: claudeAnswer,
		},
		Pinned: "claude 2.1.282, pinned 2026-09-27",
	},
	{Name: "codex", Bin: "codex", Flags: []string{"exec", "--skip-git-repo-check"}, ModelArgs: []string{"-m"},
		Structured: &Structured{
			Args: func(_, schemaPath, answerPath string) []string {
				return []string{"--json", "--output-schema", schemaPath, "-o", answerPath}
			},
			Answer: codexAnswer,
		},
		Pinned: "codex-cli 0.154.0, pinned 2026-09-23",
	},
}

// claudeAnswer reads the final envelope in the `--output-format stream-json`
// stream. The answer is `structured_output`; when the request failed, the
// sentence a person needs is in `result`, and it is worth going after — that
// run exits non-zero, so without this the reason would be reported as "exit
// code 1".
//
// The envelope is one line of JSON, but it is not the only thing on the stream
// we read: a detached run's log merges stderr, where this CLI prints warnings
// of its own. So the last line that parses as the envelope is the answer, and
// prose is never interpreted.
//
// Verified against Claude Code 2.1.282: a successful stream ends with a result
// frame carrying both `result` and `structured_output`, and a failed run still
// carries its explanation in `result`.
func claudeAnswer(stdout, _ string) ([]byte, error) {
	type envelope struct {
		StructuredOutput json.RawMessage `json:"structured_output"`
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
	}
	var last *envelope
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e envelope
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.StructuredOutput != nil || e.IsError || e.Result != "" {
			last = &e
		}
	}
	if last == nil {
		return nil, fmt.Errorf("%w: the reply carried no result object", ErrNoDraft)
	}
	if out := bytes.TrimSpace(last.StructuredOutput); len(out) > 0 && string(out) != "null" {
		return out, nil
	}
	if last.IsError || strings.TrimSpace(last.Result) != "" {
		// No sentinel on purpose: the caller wraps this into the run's failure,
		// and a second "the agent exited with an error" would bury the reason.
		if sentence := firstLine(last.Result); sentence != "" {
			return nil, errors.New(sentence)
		}
		return nil, fmt.Errorf("%w: the request failed", ErrNoDraft)
	}
	return nil, fmt.Errorf("%w: the reply carried no structured output", ErrNoDraft)
}

// codexAnswer reads the file `-o` wrote: with --output-schema, its contents are
// the answer document itself.
//
// Verified 2026-09-24 against codex-cli 0.154.0: `codex exec --json
// --output-schema s.json -o a.json …` leaves exactly `{"name":"berth"}` in
// a.json (no trailing newline) and a stream of events on stdout.
func codexAnswer(_, answerPath string) ([]byte, error) {
	out, err := os.ReadFile(answerPath)
	if err != nil {
		return nil, fmt.Errorf("%w: no answer file was written (%v)", ErrNoDraft, err)
	}
	return bytes.TrimSpace(out), nil
}

// Supported returns the init draft agent names, in matrix order.
func Supported() []string {
	out := make([]string, 0, len(clis))
	for _, c := range clis {
		out = append(out, c.Name)
	}
	return out
}

// Bins returns the program names auto-detection looks for, in matrix order.
func Bins() []string {
	out := make([]string, 0, len(clis))
	for _, c := range clis {
		out = append(out, c.Bin)
	}
	return out
}

// Lookup finds one CLI by its init draft name.
func Lookup(name string) (CLI, bool) {
	for _, c := range clis {
		if c.Name == name {
			return c, true
		}
	}
	return CLI{}, false
}

// Detect returns the supported CLIs whose program resolves on PATH, in matrix
// order. env is the environment the check reads PATH from; nil means this
// process's own — the same convention spawn.CheckRunnable uses, and the same
// judgment, so "claude is not on PATH" and "claude is on PATH at … but cannot
// be run" stay one implementation.
func Detect(env []string) []CLI {
	var out []CLI
	for _, c := range clis {
		if spawn.CheckRunnable(c.Bin, env) == nil {
			out = append(out, c)
		}
	}
	return out
}

// Argv is the full command line for one prompt: the program, the CLI's own
// flags, the model flag when one is asked for, the flags that ask for a
// schema-constrained answer, the user's extra arguments (config agent.args),
// then the prompt last.
//
// The model and the structured-output flags go before the extra arguments on
// purpose: when both name one, the later flag is the one a CLI keeps, and
// `agent.args` is the hand-written escape hatch — a person's explicit argument
// should win over the panel's.
//
// The prompt goes last for the same kind of reason. Both CLIs read their
// options and the prompt positionally (`claude [options] [prompt]`,
// `codex exec [OPTIONS] [PROMPT]`), and a prompt is arbitrary text that could
// begin with a dash — after the extra arguments there is nothing left for it
// to be mistaken for.
func (c CLI) Argv(extra []string, model, prompt string, structured []string) []string {
	argv := make([]string, 0, len(c.Flags)+len(c.ModelArgs)+len(extra)+len(structured)+3)
	argv = append(argv, c.Bin)
	argv = append(argv, c.Flags...)
	if model != "" && len(c.ModelArgs) > 0 {
		argv = append(argv, c.ModelArgs...)
		argv = append(argv, model)
	}
	argv = append(argv, structured...)
	argv = append(argv, extra...)
	return append(argv, prompt)
}
