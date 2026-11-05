// Package spawn launches a child for `oberth start`, `oberth up` and the
// daemon's `runs.spawn` so the whole tree stays killable and attributable: the
// child gets its own process group (a Job Object on Windows), the run identity
// in its environment, and, when detached, a rotated log under
// ~/.option-berth/logs.
//
// The package knows nothing about the daemon or the run registry: callers
// register the returned Handle wherever it belongs.
package spawn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// Environment variables every child receives, so a tool can read back which
// run it belongs to.
const (
	EnvGroup    = "BERTH_GROUP"
	EnvName     = "BERTH_NAME"
	EnvRunID    = "BERTH_RUN_ID"
	EnvPortHint = "BERTH_PORT"
	// EnvSession and EnvSessionLabel re-export the caller's agent session in
	// the `<tool>:<id>` form. A nested `oberth start` therefore reads the
	// session explicitly instead of detecting it again, and every run of one
	// agent shares one id.
	EnvSession      = "BERTH_SESSION"
	EnvSessionLabel = "BERTH_SESSION_LABEL"
)

// Request is one command to launch.
type Request struct {
	// Argv is the command plus its arguments, run directly rather than through
	// a shell.
	Argv []string

	// Cwd is the working directory; empty inherits the caller's.
	Cwd string
	// Env is the base environment; nil means os.Environ(). The BERTH_* run
	// variables are appended either way.
	Env []string
	// Group and Name identify the run. Callers with flags to honour run
	// Resolve first; Spawn never infers.
	Group string
	Name  string
	// PortHint is the port the command is expected to bind, or 0.
	PortHint int
	// Session is the agent session that asked for the run. Spawn only carries
	// it: BERTH_SESSION goes into the child's environment, and the Handle
	// hands it back for the registry to store. Zero means no session.
	Session state.Session
	// Detach runs the child in its own session, with stdout and stderr in the
	// run's log file, so it survives its starter.
	Detach bool

	// Identity and output.
	ID string
	// LogPath overrides the detached log file. Empty uses LogPath(group, name).
	LogPath string
	// Stdin, Stdout and Stderr apply to attached mode only; nil inherits the
	// caller's streams.
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// Handle is a started run: what the registry needs, plus the process.
type Handle struct {
	// Registry identity.
	ID   string
	PID  int
	PPID int

	// Manifest placement.
	Group    string
	Name     string
	Cmd      string
	Cwd      string
	PortHint int

	// Launch facts.
	StartedAt time.Time
	Detached  bool
	Session   state.Session

	// LogPath and LogOffset: the file is appended across runs, so this run's
	// output begins at LogOffset.
	LogPath   string
	LogOffset int64

	cmd *exec.Cmd
	log *os.File
}

// Spawn starts the request. The Handle is live: attached callers Wait on it,
// detached callers register it and return.
func Spawn(ctx context.Context, req Request) (*Handle, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(req.Argv) == 0 || strings.TrimSpace(req.Argv[0]) == "" {
		return nil, errors.New("spawn: no command given")
	}
	cwd, err := resolveWorkingDir(req.Cwd)
	if err != nil {
		return nil, err
	}

	id := req.ID
	if id == "" {
		id = NewID()
	}

	handle := &Handle{
		Group:     req.Group,
		Name:      req.Name,
		ID:        id,
		PPID:      os.Getpid(),
		StartedAt: time.Now(),
		Cmd:       strings.Join(req.Argv, " "),
		Cwd:       cwd,
		PortHint:  req.PortHint,
		Detached:  req.Detach,
		Session:   req.Session,
	}

	environment := childEnv(req, id)
	if err := CheckRunnableInDir(req.Argv[0], cwd, environment); err != nil {
		return nil, err
	}

	command := exec.Command(req.Argv[0], req.Argv[1:]...)
	// The context bounds the start, not the child's life: a detached run
	// outlives every context its caller has.
	if err := contextProblem(ctx); err != nil {
		return nil, err
	}
	command.Dir = cwd
	command.Env = environment

	if req.Detach {
		if err := redirectToLog(handle, command, req); err != nil {
			return nil, err
		}
	} else {
		command.Stdin = firstReader(req.Stdin, os.Stdin)
		command.Stdout = firstWriter(req.Stdout, os.Stdout)
		command.Stderr = firstWriter(req.Stderr, os.Stderr)
	}

	// Platform-specific: own process group or session on Unix, Job Object and
	// a new console group on Windows.
	configure(command, req.Detach)

	// Log setup and executable/cwd inspection can outlive the first check.
	// Before Start no child owns these resources, so cancellation closes them.
	if ctx != nil && ctx.Err() != nil {
		closeIfOpen(handle.log)
		return nil, ctx.Err()
	}
	if err := command.Start(); err != nil {
		closeIfOpen(handle.log)
		return nil, fmt.Errorf("spawn: starting %q: %w", req.Argv[0], err)
	}
	if err := adopt(command, req.Detach); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait() // a failed platform handover must still reap the child
		closeIfOpen(handle.log)
		return nil, fmt.Errorf("spawn: %w", err)
	}

	handle.cmd = command
	handle.PID = command.Process.Pid
	if handle.log != nil && req.Detach {
		// The child owns the descriptor now.
		handle.log.Close()
		handle.log = nil
	}
	return handle, nil
}

// resolveWorkingDir turns the request's cwd into an absolute existing
// directory.
func resolveWorkingDir(cwd string) (string, error) {
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("spawn: resolving the working directory: %w", err)
		}
		cwd = wd
	}
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return "", fmt.Errorf("spawn: %s is not a directory", cwd)
	}
	return cwd, nil
}

// redirectToLog opens the detached run's log, records where this run's output
// begins, and points both output streams at it.
func redirectToLog(handle *Handle, command *exec.Cmd, req Request) error {
	logPath := req.LogPath
	if logPath == "" {
		logPath = LogPath(req.Group, req.Name)
	}
	file, err := OpenLog(logPath)
	if err != nil {
		return err
	}
	handle.LogPath, handle.log = logPath, file
	// After any rotation OpenLog did, so a fresh file starts at zero.
	if info, err := file.Stat(); err == nil {
		handle.LogOffset = info.Size()
	}
	command.Stdin = nil
	command.Stdout = file
	command.Stderr = file
	return nil
}

func firstReader(preferred io.Reader, fallback *os.File) io.Reader {
	if preferred != nil {
		return preferred
	}
	return fallback
}

func firstWriter(preferred io.Writer, fallback *os.File) io.Writer {
	if preferred != nil {
		return preferred
	}
	return fallback
}

func closeIfOpen(file *os.File) {
	if file != nil {
		file.Close()
	}
}

// childEnv is the caller's environment plus the run identity. Stale BERTH_*
// values and a stale session are dropped first, so a nested start cannot
// inherit another run's identity.
func childEnv(req Request, id string) []string {
	base := req.Env
	if base == nil {
		base = os.Environ()
	}
	replace := map[string]bool{
		EnvGroup: true, EnvName: true, EnvRunID: true, EnvPortHint: true,
	}
	if req.Session.ID != "" {
		replace[EnvSession] = true
		replace[EnvSessionLabel] = true
	}
	out := make([]string, 0, len(base)+4)
	for _, pair := range base {
		if key, _, ok := strings.Cut(pair, "="); ok && replace[key] {
			continue
		}
		out = append(out, pair)
	}
	return append(out, identityPairs(req, id)...)
}

// identityPairs is the BERTH_* block appended after the caller's environment.
func identityPairs(req Request, id string) []string {
	pairs := []string{
		EnvGroup + "=" + req.Group,
		EnvName + "=" + req.Name,
		EnvRunID + "=" + id,
	}
	if req.PortHint > 0 {
		pairs = append(pairs, EnvPortHint+"="+strconv.Itoa(req.PortHint))
	}
	if req.Session.ID != "" {
		pairs = append(pairs, EnvSession+"="+req.Session.Tool+":"+req.Session.ID)
		if req.Session.Label != "" {
			pairs = append(pairs, EnvSessionLabel+"="+req.Session.Label)
		}
	}
	return pairs
}

func contextProblem(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// Wait blocks until the child exits and returns the exit code the caller should
// exit with: the child's own, or 128+signal when it was killed.
func (h *Handle) Wait() (int, error) {
	if h == nil || h.cmd == nil {
		return 0, errors.New("spawn: no process to wait for")
	}
	err := h.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			return code, nil
		}
		return signalExitCode(exitErr), nil
	}
	return 1, err
}

// Signal forwards sig to the child's whole process group, so a dev server's
// own children go down with it.
func (h *Handle) Signal(sig os.Signal) error {
	if h == nil || h.cmd == nil || h.cmd.Process == nil {
		return errors.New("spawn: no process to signal")
	}
	return signalGroup(h.cmd.Process, sig)
}

// Kill terminates the child's process group (or Job Object) without waiting.
func (h *Handle) Kill() error {
	if h == nil || h.cmd == nil || h.cmd.Process == nil {
		return errors.New("spawn: no process to kill")
	}
	return killGroup(h.cmd.Process)
}

// Process is the underlying child, for callers that need os.Process directly.
func (h *Handle) Process() *os.Process {
	if h == nil || h.cmd == nil {
		return nil
	}
	return h.cmd.Process
}

// NewID returns a short random run id, the same shape `option-berth run --id` uses.
func NewID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run%d", os.Getpid())
	}
	return hex.EncodeToString(b[:])
}

// Forwarder relays the interrupts this process receives to a child's process
// group (daemon spec, `oberth start` step 5).
//
// It must be created *before* the child is started. Installing the handler
// first is what guarantees the child starts with a default SIGINT disposition:
// exec turns a caught signal into the default action, while an ignored one
// stays ignored — and a `oberth start` line in a `dev.sh` is a background job,
// so it inherits an ignored SIGINT from the shell. Without this, Ctrl+C would
// reach neither option-berth nor the dev server it is supervising.
type Forwarder struct {
	ch   chan os.Signal
	stop chan struct{}
	once sync.Once

	mu      sync.Mutex
	handle  *Handle
	pending []os.Signal
	// caught records that an interrupt arrived: the child was stopped by the
	// user rather than exiting on its own.
	caught bool
}

// Interrupted reports whether an interrupt reached this process while it
// supervised the child, so the child's exit was asked for.
func (f *Forwarder) Interrupted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caught
}

// CatchSignals starts catching SIGINT and SIGTERM. Nothing is forwarded until
// Forward names the child; signals that arrive before then are replayed to it,
// so a Ctrl+C in the moment between fork and registration is not lost.
func CatchSignals() *Forwarder {
	f := &Forwarder{ch: make(chan os.Signal, 8), stop: make(chan struct{})}
	signal.Notify(f.ch, os.Interrupt, syscall.SIGTERM)
	go f.run()
	return f
}

// Forward starts sending caught signals to h's process group.
func (f *Forwarder) Forward(h *Handle) {
	f.mu.Lock()
	f.handle = h
	pending := f.pending
	f.pending = nil
	f.mu.Unlock()
	for _, sig := range pending {
		_ = h.Signal(sig)
	}
}

// Stop stops catching signals and restores the default behaviour.
func (f *Forwarder) Stop() {
	f.once.Do(func() {
		signal.Stop(f.ch)
		close(f.stop)
	})
}

func (f *Forwarder) run() {
	for {
		select {
		case sig := <-f.ch:
			f.mu.Lock()
			f.caught = true
			h := f.handle
			if h == nil {
				f.pending = append(f.pending, sig)
			}
			f.mu.Unlock()
			if h != nil {
				_ = h.Signal(sig)
			}
		case <-f.stop:
			return
		}
	}
}
