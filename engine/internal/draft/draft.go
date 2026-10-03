package draft

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/paths"
	"github.com/sheathedsharp/option-berth/internal/spawn"
)

// The ways a drafting run fails, for the caller to classify. Everything else
// that comes back (parse errors above all) is already a typed error of its own.
var (
	// ErrTimeout — the run hit its ceiling and was killed.
	ErrTimeout = errors.New("the agent did not finish in time")
	// ErrAgentFailed — the agent exited non-zero.
	ErrAgentFailed = errors.New("the agent exited with an error")
	// ErrNoDraft — the run produced no usable answer document.
	ErrNoDraft = errors.New("the reply had no draft")
)

// Request is one run.
type Request struct {
	// CLI is the agent to run, already resolved.
	CLI CLI
	// Root is the project directory the agent runs in.
	Root string
	// Prompt is the whole brief (rules plus facts).
	Prompt string
	// Schema is the JSON Schema the answer must conform to. Empty means this
	// is a drafting run and DraftSchema applies; the judgment road passes a
	// schema rendered from its own questions.
	Schema string
	// Extra are the user's arguments (config agent.args), before the prompt.
	Extra []string
	// Model is the model name to hand the CLI (config agent.model), through
	// the CLI's own flag. Empty means the CLI's own default.
	Model string
	// Timeout is the ceiling; zero means no ceiling beyond the context.
	Timeout time.Duration
	// Stream receives the agent's raw output while it works. The command line
	// leaves it nil so `init draft` produces one draft result; tests may use it
	// to capture a run without touching the terminal.
	Stream io.Writer
	// Group and Name give the run its BERTH_* environment.
	Group string
	Name  string
	// DryRun skips the archive — the draft is returned, not kept anywhere.
	DryRun bool
}

// schema is the schema the CLI is handed: the caller's, or the draft's.
func (r Request) schema() string {
	if strings.TrimSpace(r.Schema) != "" {
		return r.Schema
	}
	return DraftSchema
}

// Result is what one run produced. It is returned alongside an error when
// there is something to show (the agent's reply, say) even though the run
// failed.
type Result struct {
	// Draft is the yaml block, verbatim.
	Draft string
	// Reply is the agent's whole output, both streams, in order.
	Reply string
	// Path is where the draft was archived ("" with DryRun).
	Path string
	// Config is the draft as the same loader that reads a real manifest parses
	// it — validated before anything offers to adopt it.
	Config *groups.Config
}

// Run starts the agent, waits for it, and turns its answer into a draft.
//
// The child gets its own process group and the interrupt handling gives the
// agent a working Ctrl+C. Its output is kept for the failure report.
func Run(ctx context.Context, req Request) (*Result, error) {
	answer, res, err := Ask(ctx, req)
	if err != nil {
		return res, err
	}
	return finishFromAnswer(res, answer, req)
}

// Ask runs the agent for one schema-constrained answer and hands back that
// answer verbatim, with the raw reply alongside it.
//
// It is the whole run minus "what the answer means": Run puts a draft on top of
// it, and the judgment road — same CLI, same schema-constrained answer, a
// different document — decodes its own. Everything that can fail is reported
// the same way for both, which is the point of the split.
func Ask(ctx context.Context, req Request) (answer []byte, res *Result, err error) {
	// A caller that has no context (a direct call, a test) is a caller with
	// nothing to cancel: spawn tolerates a nil context, so the polling below
	// has to as well.
	if ctx == nil {
		ctx = context.Background()
	}
	if req.CLI.Structured == nil {
		return nil, nil, fmt.Errorf("%w: %s cannot answer in a schema", ErrNoDraft, req.CLI.Name)
	}
	stream := req.Stream
	if stream == nil {
		stream = io.Discard
	}

	// The schema and the answer travel through a directory of their own: one of
	// the CLIs takes the schema as a path and writes the answer to a path, and
	// a temporary directory keeps both out of the project being drafted.
	dir, err := os.MkdirTemp("", "berth-draft-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	files := answerFilesIn(dir)
	if err := files.writeSchema(req.schema()); err != nil {
		return nil, nil, err
	}

	// stdout and stderr stay separate because the answer is read from stdout;
	// merging a warning line into it would make a valid agent response fail.
	var stdout, stderr strings.Builder
	fwd := spawn.CatchSignals()
	defer fwd.Stop()

	argv := req.CLI.Argv(req.Extra, req.Model, req.Prompt,
		req.CLI.Structured.Args(req.schema(), files.schema, files.answer))
	h, err := spawn.Spawn(ctx, spawn.Request{
		Argv:   argv,
		Cwd:    req.Root,
		Group:  req.Group,
		Name:   req.Name,
		Stdout: io.MultiWriter(&stdout, stream),
		Stderr: io.MultiWriter(&stderr, stream),
	})
	if err != nil {
		return nil, nil, err
	}
	fwd.Forward(h)

	// The timeout kills the whole process group. The context spawn was given
	// bounds the start only — a child that is running is the caller's to stop,
	// and this is the stop.
	var timedOut atomic.Bool
	var timer *time.Timer
	if req.Timeout > 0 {
		timer = time.AfterFunc(req.Timeout, func() {
			timedOut.Store(true)
			_ = h.Kill()
		})
	}
	code, waitErr := h.Wait()
	if timer != nil {
		timer.Stop()
	}
	res = &Result{Reply: stdout.String() + stderr.String()}
	answer, answerErr := req.CLI.Structured.Answer(stdout.String(), files.answer)
	switch {
	case fwd.Interrupted() || errors.Is(ctx.Err(), context.Canceled):
		// The person stopped it: that is not an agent failure, and the caller
		// renders it as the interrupt it was (exit 130).
		return nil, res, context.Canceled
	case timedOut.Load():
		return nil, res, fmt.Errorf("%w (after %s)", ErrTimeout, req.Timeout)
	case waitErr != nil:
		return nil, res, waitErr
	case code != 0:
		// A CLI can explain itself and still exit non-zero — claude prints the
		// reason on stdout and exits 1 — so its own account is worth more than
		// the exit code on its own.
		if answerErr != nil {
			return nil, res, fmt.Errorf("%w (exit code %d): %v", ErrAgentFailed, code, answerErr)
		}
		return nil, res, fmt.Errorf("%w (exit code %d)", ErrAgentFailed, code)
	}
	if answerErr != nil {
		return nil, res, answerErr
	}
	return answer, res, nil
}

// answers are the two files a structured run needs: the schema the CLI is
// given, and the answer it is told to write (for the CLIs that take a path
// rather than answering on stdout).
type answers struct{ schema, answer string }

// answerFilesIn puts them in a directory of the run's own (Run's temporary one).
func answerFilesIn(dir string) answers {
	return answers{
		schema: filepath.Join(dir, "schema.json"),
		answer: filepath.Join(dir, "answer.json"),
	}
}

// writeSchema puts the schema where a CLI that wants a path can read it,
// creating the directory if it is not there yet — a detached run's log
// directory is made by spawn, which has not run at this point.
func (a answers) writeSchema(schema string) error {
	if dir := filepath.Dir(a.schema); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(a.schema, []byte(schema), 0o600)
}

// finishFromAnswer is what a successful run's answer becomes: a manifest
// rendered from it, parsed by the loader that will read it if it is adopted,
// archived where init adopt looks (unless the caller asked for a dry run).
// Run and Finish both end here, so "what counts as a draft" has one answer.
func finishFromAnswer(res *Result, answer []byte, req Request) (*Result, error) {
	d, err := ParseDraft(answer)
	if err != nil {
		return res, err
	}
	draftText, err := d.YAML()
	if err != nil {
		return res, err
	}
	// Parsed with the loader that will read it if it is adopted, addressed by
	// the path it will live at — so an invalid draft reports line numbers
	// against a file that exists.
	archivePath := paths.Draft(req.Group)
	cfg, err := groups.Parse(archivePath, draftText)
	if err != nil {
		return res, err
	}
	res.Draft, res.Config = string(draftText), cfg

	if req.DryRun {
		return res, nil
	}
	if err := archive(archivePath, req, res.Draft); err != nil {
		return res, err
	}
	res.Path = archivePath
	return res, nil
}

// archive writes the draft where `init adopt` looks for it, under a header
// that says where it came from and what adopting it means.
//
// The header is comments only, and the file is written atomically: the same
// loader reads it at adoption time, so a half-written draft must never be
// there to read.
func archive(path string, req Request, draftText string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	header := fmt.Sprintf(
		"# Drafted by %s (%s) on %s, from %s.\n"+
			"# A draft, not a declaration: nothing here is a service until you adopt it —\n"+
			"#   oberth init adopt\n"+
			"# writes it into the project's oberth.yaml.\n\n",
		req.CLI.Name, req.CLI.Pinned, time.Now().Format("2006-01-02 15:04"), req.Root)

	tmp, err := os.CreateTemp(filepath.Dir(path), ".draft-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(header + draftText); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
