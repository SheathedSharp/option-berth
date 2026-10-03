package runsreg

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/spawn"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
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
	// lastLineCount and tailBytes bound the log lines kept with an exit.
	lastLineCount = 20
	tailBytes     = 16 << 10
	// stopWindow is how long after an exit a kill may still correct it to
	// stopped: the kill can win the race against being marked as one.
	stopWindow = 30 * time.Second
)

// Meta is what a caller knows about where a run comes from, beyond the spawn
// request itself.
type Meta struct {
	ConfigPath string
	StartID    string
	Origin     string
	SpecHash   string
}

// Origin is the client a request came from, as it named itself in
// daemon.hello: cli, app, mcp or tray.
func Origin(req *daemon.Request) string {
	if req == nil || req.Conn == nil {
		return ""
	}
	name, _, _ := req.Conn.Client()
	return name
}

// Exit is a run that ended: the record it had, and how it ended.
type Exit struct {
	Record
	Code      int
	Reason    string
	ExitedAt  time.Time
	LastLines []string
}

// ServiceExit is the exit as a service row carries it.
func (e Exit) ServiceExit() state.ServiceExit {
	return state.ServiceExit{
		Code:   e.Code,
		Reason: e.Reason,
		At:     e.ExitedAt.UTC().Format(time.RFC3339),
		RunID:  e.ID,
	}
}

func exitReason(code int, stopped bool) string {
	switch {
	case stopped:
		return ReasonStopped
	case code == 0:
		return ReasonExited
	default:
		return ReasonCrashed
	}
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
func (r *Registry) StartFailedWithReason(rec Record, reason, message string) Exit {
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
	for _, rec := range r.runs {
		if rec.Group == group && rec.Name == service {
			return state.ServiceExit{}, false
		}
	}
	for i := len(r.exits) - 1; i >= 0; i-- {
		if e := r.exits[i]; e.Group == group && e.Name == service {
			return e.ServiceExit(), true
		}
	}
	return state.ServiceExit{}, false
}

// Exits returns the exit history, newest first.
func (r *Registry) Exits() []Exit {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Exit, 0, len(r.exits))
	for i := len(r.exits) - 1; i >= 0; i-- {
		out = append(out, r.exits[i])
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
	for i := len(r.exits) - 1; i >= 0; i-- {
		if r.exits[i].Group == group && r.exits[i].Name == service {
			return r.exits[i], true
		}
	}
	return Exit{}, false
}

// ExitForPID returns the recorded exit of one process. The startup window uses
// this instead of LatestExit: when two runs of one service overlap, the newest
// exit for the service name can belong to the other run — reporting a live
// spawn as failed. An exit belongs to the run it names, by pid.
func (r *Registry) ExitForPID(pid int) (Exit, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.exits) - 1; i >= 0; i-- {
		if r.exits[i].PID == pid {
			return r.exits[i], true
		}
	}
	return Exit{}, false
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

// tailLines returns the last n lines a run wrote to its log: from offset, where
// the run began, or from the top of a file that was rotated since. At most
// tailBytes are read; a line cut by that limit is dropped.
func tailLines(path string, offset int64, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	size := info.Size()
	if offset > size {
		offset = 0
	}
	start := offset
	if size-start > tailBytes {
		start = size - tailBytes
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	text := strings.TrimRight(string(buf), "\r\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if start > offset && len(lines) > 1 {
		lines = lines[1:]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return lines
}

// serviceURL is the address a run's port hint stands for.
func serviceURL(port int) string { return "http://localhost:" + strconv.Itoa(port) }

// exitRow renders one exit for `runs.list`.
func exitRow(e Exit) rpc.RunRecord {
	code := e.Code
	out := rpc.RunRecord{
		ID:         e.ID,
		PID:        e.PID,
		Group:      e.Group,
		Name:       e.Name,
		Cmd:        e.Cmd,
		Cwd:        e.Cwd,
		StartedAt:  e.StartedAt.Format(time.RFC3339),
		Ports:      []int{},
		Status:     "exited",
		ConfigPath: e.ConfigPath,
		StartID:    e.StartID,
		Origin:     e.Origin,
		SpecHash:   e.SpecHash,
		LogPath:    e.LogPath,
		ExitCode:   &code,
		Reason:     e.Reason,
		ExitedAt:   e.ExitedAt.UTC().Format(time.RFC3339),
		LastLines:  e.LastLines,
	}
	if e.PortHint > 0 {
		hint := e.PortHint
		out.PortHint = &hint
		out.URL = serviceURL(hint)
	}
	return out
}
