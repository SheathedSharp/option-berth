package runsreg

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/spawn"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// How a run ended. A run option-berth or the user asked to stop is stopped whatever
// its exit code; otherwise code 0 is exited, a bind error is port_occupied, and
// anything else is crashed.
const (
	ReasonExited             = "exited"
	ReasonCrashed            = "crashed"
	ReasonStopped            = "stopped"
	ReasonPortOccupied       = "port_occupied"
	ReasonStartFailed        = "start_failed"
	ReasonReadyTimeout       = "ready_timeout"
	ReasonDependencyTimeout  = "dependency_timeout"
	ReasonDependencyNotReady = "dependency_not_ready"
)

const (
	// maxExits caps the exit history.
	maxExits = store.RunExitLimit

	// lastLineCount and tailBytes bound the log lines an exit keeps.
	lastLineCount = 20
	tailBytes     = 16 << 10

	// stopWindow bounds how long a kill may still correct a just-recorded exit
	// to stopped: the kill can win the race against being marked as one.
	stopWindow = 30 * time.Second
)

// Meta is what a caller knows about a run beyond the spawn request: the
// manifest it was started from, the start that grouped it with its siblings,
// the client that asked, and the identity of what ran.
type Meta struct {
	ConfigPath string
	StartID    string
	Origin     string
	SpecHash   string
}

// Origin is the client a request came from, as it named itself in
// daemon.hello. The engine's own client sends "cli"; the field is carried
// through unchanged so a future caller kind does not need a daemon change.
func Origin(req *daemon.Request) string {
	if req == nil || req.Conn == nil {
		// A request with no connection has named no client, so the run it is
		// about to register stays origin-less rather than misattributed.
		return ""
	}
	name, _, _ := req.Conn.Client()
	return name
}

// Exit is a run that ended: the record it carried while it was live, and how it
// ended.
type Exit struct {
	Record
	// Code is the process's own exit status; Reason is the normalized outcome
	// the wire publishes, which is not always what the code alone says.
	Code   int
	Reason string
	// ExitedAt and LastLines are the when and the tail: enough for a reader to
	// place a run that is gone without opening its log file.
	ExitedAt  time.Time
	LastLines []string
}

// ServiceExit projects the exit onto the row a service carries.
func (e Exit) ServiceExit() state.ServiceExit {
	return state.ServiceExit{
		Code:     e.Code,
		Reason:   e.Reason,
		At:       e.ExitedAt.UTC().Format(time.RFC3339),
		RunID:    e.ID,
		PortHint: e.PortHint,
	}
}

// exitReason names how a run ended from the two facts the exit itself carries:
// whether it was asked to stop, and the code it left.
func exitReason(code int, stopped bool) string {
	if stopped {
		return ReasonStopped
	}
	if code == 0 {
		return ReasonExited
	}
	return ReasonCrashed
}

func occupiedExit(reason string, lines []string) string {
	if reason != ReasonCrashed {
		return reason
	}
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "address already in use") || strings.Contains(lower, "address in use") || strings.Contains(lower, "eaddrinuse") {
			return ReasonPortOccupied
		}
	}
	return reason
}

func exitReasonForRecord(code int, stopped bool, rec Record) string {
	if rec.stoppingReason != "" {
		return rec.stoppingReason
	}
	return exitReason(code, stopped || rec.stopping)
}

// StartFailed records a service attempt that failed before a child process was
// registered. It uses the same exit history as a process that later exits, so
// status can distinguish "never started" from "could not start" and the
// evidence survives a daemon restart.
func (r *Registry) StartFailed(rec Record, message string) Exit {
	return r.StartFailedWithReason(rec, ReasonStartFailed, message)
}

// StartFailedWithReason records a failed attempt with the lifecycle reason
// that caused it. Dependency failures are distinct from a command that could
// not be spawned, so status, attention and event consumers see the same fact.
//
// A run that never started has ended too, so this announces it the same way a
// process exit does — after the mirror lock is released, never under it: a
// listener is other people's code, and holding this registry's lock across it
// would put their work on this registry's critical path.
func (r *Registry) StartFailedWithReason(rec Record, reason, message string) Exit {
	e := r.startFailed(rec, reason, message)
	daemon.NotifyRunExit(daemon.RunExit{
		RunID: e.ID, PID: e.PID, Code: e.Code, Reason: e.Reason, ExitedAt: e.ExitedAt,
	})
	return e
}

func (r *Registry) startFailed(rec Record, reason, message string) Exit {
	// Order the in-memory event and its durable projection with reap/rename.
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	if reason == "" {
		reason = ReasonStartFailed
	}
	if rec.ID == "" {
		rec.ID = spawn.NewID()
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = r.clock()
	}
	message = strings.TrimSpace(message)
	lines := []string{}
	if message != "" {
		for _, line := range strings.Split(message, "\n") {
			if line = strings.TrimRight(line, "\r"); line != "" {
				lines = append(lines, line)
			}
		}
	}
	e := Exit{
		Record:    rec,
		Code:      1,
		Reason:    reason,
		ExitedAt:  r.clock(),
		LastLines: lines,
	}
	r.mu.Lock()
	r.exits = append(r.exits, e)
	if len(r.exits) > maxExits {
		r.exits = append([]Exit(nil), r.exits[len(r.exits)-maxExits:]...)
	}
	history := r.history
	r.mu.Unlock()
	if history != nil {
		r.persistenceResult("exit.append", history.AppendRunExit(storeRow(e)))
	}
	return e
}

// Stopping marks runs option-berth is about to stop, so their exit is recorded as
// stopped rather than a crash. A run that already exited has its record
// corrected instead, as long as it ended moments ago.
func (r *Registry) Stopping(pids []int) {
	r.StoppingWithReason(pids, ReasonStopped)
}

// StoppingWithReason marks runs option-berth is stopping with a specific
// terminal reason. It is intentionally an additive interface used by
// lifecycle callers that need the reason to survive the asynchronous reap.
func (r *Registry) StoppingWithReason(pids []int, reason string) {
	// A correction must follow the corresponding append, not race it to SQL.
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	if reason == "" {
		reason = ReasonStopped
	}
	now := r.clock()
	var corrected []Exit
	r.mu.Lock()
	for _, pid := range pids {
		if rec, ok := r.runs[pid]; ok {
			rec.stopping = true
			rec.stoppingReason = reason
			r.runs[pid] = rec
			continue
		}
		for i := len(r.exits) - 1; i >= 0; i-- {
			if e := &r.exits[i]; e.PID == pid && !e.ExitedAt.After(now) && now.Sub(e.ExitedAt) < stopWindow {
				e.Reason = reason
				corrected = append(corrected, *e)
				break
			}
		}
	}
	history := r.history
	r.mu.Unlock()
	if history != nil {
		for _, e := range corrected {
			r.persistenceResult("exit.correct", history.CorrectRunExit(storeRow(e)))
		}
	}
}

// LastExit reports how the latest run ended while the service is down.
// Publication uses ServiceFacts to capture this together with live runs.
func (r *Registry) LastExit(group, service string) (state.ServiceExit, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasRunLocked(group, service) {
		return state.ServiceExit{}, false
	}
	e, ok := r.newestExitLocked(func(e Exit) bool { return e.Group == group && e.Name == service })
	if !ok {
		return state.ServiceExit{}, false
	}
	return e.ServiceExit(), true
}

// hasRunLocked reports whether a run of this group and service is registered at
// all, stopping or not. The caller holds mu.
func (r *Registry) hasRunLocked(group, service string) bool {
	for _, rec := range r.runs {
		if rec.Group == group && rec.Name == service {
			return true
		}
	}
	return false
}

// newestExitLocked is the one backward scan the exit queries share: the exit
// history is oldest first, so the newest match is found from its end. The caller
// holds mu.
func (r *Registry) newestExitLocked(matches func(Exit) bool) (Exit, bool) {
	for i := len(r.exits) - 1; i >= 0; i-- {
		if matches(r.exits[i]) {
			return r.exits[i], true
		}
	}
	return Exit{}, false
}

// Exits returns the recorded exits, newest first.
func (r *Registry) Exits() []Exit {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Exit, len(r.exits))
	for i, e := range r.exits {
		out[len(out)-1-i] = e
	}
	return out
}

// LatestExit returns the most recent finished run for one service, including
// its exit code and log tail. It is used by groups.start to turn a process that
// dies during the startup window into a service-level error in the same call,
// while status continues to expose the durable exit record afterwards.
func (r *Registry) LatestExit(group, service string) (Exit, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.newestExitLocked(func(e Exit) bool { return e.Group == group && e.Name == service })
}

// ExitForPID returns the recorded exit of one process. The startup window uses
// this instead of LatestExit: when two runs of one service overlap, the newest
// exit for the service name can belong to the other run — reporting a live
// spawn as failed. An exit belongs to the run it names, by pid.
func (r *Registry) ExitForPID(pid int) (Exit, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.newestExitLocked(func(e Exit) bool { return e.PID == pid })
}

func storeRow(e Exit) store.RunExitRow {
	return store.RunExitRow{
		ID: e.ID, PID: e.PID, Group: e.Group, Name: e.Name, Cmd: e.Cmd,
		Cwd: e.Cwd, PortHint: e.PortHint, StartedAt: e.StartedAt,
		ConfigPath: e.ConfigPath, StartID: e.StartID, Origin: e.Origin,
		LogPath: e.LogPath, LogOffset: e.LogOffset, Code: e.Code,
		Reason: e.Reason, ExitedAt: e.ExitedAt, LastLines: e.LastLines,
	}
}

func exitFromStore(row store.RunExitRow) Exit {
	return Exit{
		Record: Record{
			ID: row.ID, PID: row.PID, Group: row.Group, Name: row.Name,
			Cmd: row.Cmd, Cwd: row.Cwd, PortHint: row.PortHint,
			StartedAt: row.StartedAt, ConfigPath: row.ConfigPath,
			StartID: row.StartID, Origin: row.Origin, LogPath: row.LogPath,
			LogOffset: row.LogOffset,
		},
		Code: row.Code, Reason: row.Reason, ExitedAt: row.ExitedAt,
		LastLines: append([]string(nil), row.LastLines...),
	}
}

// tailLines returns the last n lines a run wrote to its log: counted from
// offset, where the run began, or from the top of a file that was rotated since.
// A line cut by the tailBytes window is dropped rather than half-reported.
func tailLines(path string, offset int64, n int) []string {
	tail, ok := readTail(path, offset)
	if !ok {
		return nil
	}
	text := strings.TrimRight(string(tail.data), "\r\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if tail.start > tail.from && len(lines) > 1 {
		// The read did not open on a line boundary, so its first entry is the
		// tail of a line the reader never saw the start of.
		lines = lines[1:]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, "\r")
	}
	return lines
}

// logTail is the part of a log file one tail is read from. from is where the
// run's own output begins and start is where the read actually opened, which
// differs when the file outgrew the tailBytes window.
type logTail struct {
	data  []byte
	start int64
	from  int64
}

// readTail reads the bytes a tail may come from. A missing or unreadable file
// has no tail at all.
func readTail(path string, offset int64) (logTail, bool) {
	f, err := os.Open(path)
	if err != nil {
		return logTail{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return logTail{}, false
	}
	size := info.Size()
	if offset > size {
		// The file is shorter than where the run began: it was rotated, so the
		// whole of it belongs to this run.
		offset = 0
	}
	tail := logTail{from: offset, start: offset}
	if size-tail.start > tailBytes {
		tail.start = size - tailBytes
	}
	tail.data = make([]byte, size-tail.start)
	if _, err := f.ReadAt(tail.data, tail.start); err != nil && !errors.Is(err, io.EOF) {
		return logTail{}, false
	}
	return tail, true
}

// serviceURL is the address a run's port hint stands for.
func serviceURL(port int) string { return "http://localhost:" + strconv.Itoa(port) }

// exitRow renders one exit for `runs.list`. ports supplies the one read-time
// fact the durable exit row cannot carry: who is on the declared port now. A
// bind failure is explained by a listener that is still there; if it has since
// released the port, there is no holder to name.
func exitRow(e Exit, ports []state.Port) rpc.RunRecord {
	code := e.Code
	// The run's own facts — identity, provenance, where it logs — are the same
	// block a live row carries; only how it ended is new here.
	out := liveRow(e.Record)
	out.Ports = []int{}
	out.Status = "exited"
	out.ExitCode = &code
	out.Reason = e.Reason
	out.ExitedAt = e.ExitedAt.UTC().Format(time.RFC3339)
	out.LastLines = e.LastLines
	if e.PortHint > 0 {
		hint := e.PortHint
		out.PortHint = &hint
		out.URL = serviceURL(hint)
		if e.Reason == ReasonPortOccupied {
			out.PortHolder = servicefacts.FindPortHolder(ports, servicefacts.PortHolderQuery{
				Port: hint, Group: e.Group, Service: e.Name, RunID: e.ID,
			})
		}
	}
	return out
}
