package agentstream

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/agentlaunch"
)

// Options configures a structured agent bridge process.
type Options struct {
	Provider   string
	Worktree   string
	ResumeFile string
	// LookPath resolves provider executables; nil uses exec.LookPath.
	LookPath func(string) (string, error)
	// Observer, when set, receives each protocol event after it has been
	// written to out. It is for local bookkeeping beside the bridge — the
	// workplace record is what uses it — and must neither block nor write to
	// out: the protocol belongs to the caller, and this is a side channel.
	Observer func(event map[string]any)
}

// Supported reports whether this build includes a stream adapter for the provider.
func Supported(provider string) bool {
	switch provider {
	case "codex", "claude", "opencode", "pi":
		return true
	default:
		return false
	}
}

// Run serves NDJSON commands on in and events on out until stdin closes or session ends.
func Run(opts Options, in io.Reader, out io.Writer, errOut io.Writer) error {
	if !Supported(opts.Provider) {
		return fmt.Errorf("unsupported provider %q for agent stream", opts.Provider)
	}
	abs, err := resolveWorktree(opts.Worktree)
	if err != nil {
		return err
	}
	if opts.ResumeFile != "" {
		if _, err := agentlaunch.Build(agentlaunch.Options{
			Provider: opts.Provider, Worktree: abs, Mode: "native", ResumeFile: opts.ResumeFile,
		}, func(string) (string, error) { return "/bin/true", nil }); err != nil {
			return err
		}
	}
	driver, err := newDriver(opts.Provider, abs, opts.ResumeFile, os.Getenv("BERTH_AGENT_STREAM_FAKE"), opts.LookPath)
	if err != nil {
		return err
	}
	bridge := &bridge{driver: driver, out: &lineWriter{w: out}, observer: opts.Observer}
	commands := bufio.NewReader(in)
	for {
		cmd, err := readCommand(commands)
		if err == io.EOF {
			return bridge.closeSession()
		}
		if err != nil {
			return err
		}
		if err := bridge.handle(cmd); err != nil {
			if errOut != nil {
				fmt.Fprintln(errOut, err.Error())
			}
			return err
		}
	}
}

func versionOne(v any) bool {
	switch n := v.(type) {
	case float64:
		return n == 1
	case int:
		return n == 1
	case int64:
		return n == 1
	default:
		return false
	}
}

func resolveWorktree(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("an explicit --worktree directory is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("cannot resolve selected worktree: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("selected worktree is not a directory")
	}
	return abs, nil
}

type bridge struct {
	driver   driver
	out      *lineWriter
	observer func(event map[string]any)
	closed   bool
	emitMu   sync.Mutex
}

func (b *bridge) handle(cmd map[string]any) error {
	b.emitMu.Lock()
	err := b.out.err
	b.emitMu.Unlock()
	if err != nil {
		return err
	}
	typ, _ := cmd["type"].(string)
	id, _ := cmd["id"].(string)
	if !versionOne(cmd["v"]) {
		b.emitError(id, "invalid", "command version must be 1")
		return nil
	}
	switch typ {
	case "turn.start":
		text, _ := cmd["text"].(string)
		return b.driver.turnStart(b, id, text)
	case "turn.interrupt":
		return b.driver.turnInterrupt(b, id)
	case "request.respond":
		request, _ := cmd["request"].(string)
		decision, _ := cmd["decision"].(string)
		return b.driver.requestRespond(b, id, request, decision)
	case "session.close":
		return b.closeSession()
	default:
		b.emitError(id, "invalid", "unknown command type")
		return nil
	}
}

func (b *bridge) closeSession() error {
	b.emitMu.Lock()
	if b.closed {
		err := b.out.err
		b.emitMu.Unlock()
		return err
	}
	b.closed = true
	b.emitMu.Unlock()
	b.driver.sessionEnd(b)
	b.emitMu.Lock()
	err := b.out.err
	b.emitMu.Unlock()
	return err
}

// emit writes one event and hands it to the observer.
//
// The observer runs outside the emit lock and after the write: an observer
// that is slow delays its own bookkeeping, never the protocol line the caller
// is waiting for, and never another emitter's line.
func (b *bridge) emit(v any) {
	b.emitMu.Lock()
	b.out.emit(v)
	b.emitMu.Unlock()
	if b.observer == nil {
		return
	}
	if event, ok := v.(map[string]any); ok {
		b.observer(event)
	}
}

func (b *bridge) emitAck(id string) {
	b.emit(map[string]any{"v": 1, "type": "ack", "id": id})
}

func (b *bridge) emitError(id, code, message string) {
	b.emit(map[string]any{"v": 1, "type": "error", "id": id, "code": code, "message": message})
}

type driver interface {
	turnStart(b *bridge, id, text string) error
	turnInterrupt(b *bridge, id string) error
	requestRespond(b *bridge, id, request, decision string) error
	sessionEnd(b *bridge)
}

func newDriver(provider, worktree, resumeFile, fake string, lookPath func(string) (string, error)) (driver, error) {
	fake = strings.TrimSpace(fake)
	if fake != "" {
		if fake != provider {
			return nil, fmt.Errorf("fake stream adapter %q does not match provider %q", fake, provider)
		}
		return &fakeDriver{provider: provider, worktree: worktree, resumeFile: resumeFile}, nil
	}
	return newLiveDriver(provider, worktree, resumeFile, lookPath), nil
}
