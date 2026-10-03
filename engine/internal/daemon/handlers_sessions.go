package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/killer"
	"github.com/sheathedsharp/option-berth/internal/sessions"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// The sessions namespace (spec 2 §3). A session is an agent — Claude Code,
// Codex, Cursor — that asked option-berth to start something. The run registry is the
// authority on which sessions are live; the store remembers the rest for the
// seven days `port_history` may still name them.
//
// The collection itself is built here and handed to the scanner, so `sessions`
// rides in every snapshot and delta like the other four collections
// (contract §5) instead of being a read-only side table.
func init() {
	RegisterHandler("sessions.kill", handleSessionsKill)
	RegisterCapability("sessions")

	OnStart(func(rt *Runtime) {
		rt.Scanner.SetSessions(func(ports []state.Port) []state.SessionRecord {
			return sessionRecords(rt, ports)
		})
		startSessionMaintenance(rt)
	})
	OnShutdown(func(bool) { stopSessionMaintenance() })
}

// sessionRuns is the optional interface the installed run registry implements
// to report which agent session started each live run. It is an assertion
// rather than a method on RunRegistry because internal/daemon/runsreg imports
// this package, never the other way round (contract §8).
type sessionRuns interface {
	SessionRuns() []sessions.Live
}

// liveSessions is every live run that carries a session.
func liveSessions(rt *Runtime) []sessions.Live {
	if reg, ok := rt.Runs().(sessionRuns); ok {
		return reg.SessionRuns()
	}
	return nil
}

// knownSessions is what the store remembers, live or not.
func knownSessions(rt *Runtime) []sessions.Known {
	if rt.Store == nil {
		return nil
	}
	rows, err := rt.Store.Sessions().List()
	if err != nil {
		rt.Logger.Warn("reading recorded sessions", "error", err)
		return nil
	}
	out := make([]sessions.Known, 0, len(rows))
	for _, r := range rows {
		out = append(out, sessions.Known{
			Session:   sessionFromRow(r),
			FirstSeen: r.FirstSeen,
			LastSeen:  r.LastSeen,
		})
	}
	return out
}

func sessionFromRow(r store.SessionRow) state.Session {
	return state.Session{
		ID: r.ID, Tool: r.Tool, Label: r.Label,
		Worktree: r.Worktree, Branch: r.Branch, Detected: r.Detected,
	}
}

// sessionRecords is the `sessions` collection for one scan tick.
func sessionRecords(rt *Runtime, ports []state.Port) []state.SessionRecord {
	return sessions.Records(knownSessions(rt), liveSessions(rt), ports)
}

// handleSessionsKill stops everything one session started. It is the session
// form of `groups.kill` (contract §3) and returns the same envelope: the
// session's listening ports are the targets, plus the pid of any of its runs
// that is not listening on anything yet, so "stop everything this session
// started" also catches a dev server still coming up.
func handleSessionsKill(ctx context.Context, req *Request) (any, error) {
	var p rpc.SessionsKillParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "id is required",
			`send {"id": "claude-code:abc123"}`)
	}

	snap, err := killSnapshot(ctx, req)
	if err != nil {
		return nil, err
	}
	live := sessions.RunsOf(liveSessions(req.Runtime), id)
	targets := sessionTargets(snap, live, id, req.Runtime.Runs().RunStart)
	if len(targets) == 0 {
		if _, ok := findSession(sessionRecords(req.Runtime, snap.Ports), id); !ok {
			return nil, sessionNotFound(id)
		}
		// A known session with nothing running: an empty envelope, not an
		// error. "Stop everything this session started" succeeded trivially.
		return killEnvelope(nil), nil
	}

	opts := killer.Options{
		Tree:   p.Tree,
		Force:  p.Force,
		DryRun: p.DryRun,
		Ports:  killerRows(snap),
	}
	opts.OnSignal = stopSignalObserver(req.Runtime, snap, targets, opts.DryRun, "")
	rows := killer.KillPorts(ctx, targets, opts)
	afterKill(req, opts.DryRun)
	return killEnvelope(rows), nil
}

// sessionTargets selects what a session owns: every listening port stamped
// with it, then the root pid of any of its runs that owns none of them. runStart
// attaches the run's recorded start to a pid target so the killer can refuse a
// recycled pid; nil is allowed and means no claim.
func sessionTargets(snap state.Snapshot, live []sessions.Live, id string, runStart func(int) (time.Time, bool)) []killer.Target {
	var out []killer.Target
	covered := map[int]bool{}
	for _, p := range snap.Ports {
		if p.Session == nil || p.Session.ID != id {
			continue
		}
		out = append(out, killer.Target{Port: p.Port, BindAddress: p.BindAddress})
		if p.Run != nil {
			covered[p.Run.RootPID] = true
		}
	}
	for _, l := range live {
		if l.PID > 0 && !covered[l.PID] {
			target := killer.Target{PID: l.PID}
			if runStart != nil {
				if started, ok := runStart(l.PID); ok {
					target.StartedAt = started
				}
			}
			out = append(out, target)
		}
	}
	return out
}

// findSession matches a session by its full id, and — because the ids are long
// — by a unique prefix, which is what makes `oberth sessions <id>` usable by
// hand. An ambiguous prefix is reported rather than guessed.
func findSession(records []state.SessionRecord, id string) (state.SessionRecord, bool) {
	for _, rec := range records {
		if rec.ID == id {
			return rec, true
		}
	}
	var hit state.SessionRecord
	n := 0
	for _, rec := range records {
		if strings.HasPrefix(rec.ID, id) {
			hit, n = rec, n+1
		}
	}
	if n == 1 {
		return hit, true
	}
	return state.SessionRecord{}, false
}

func sessionNotFound(id string) error {
	return rpc.NewError(rpc.CodeSessionNotFound, "no session "+id,
		"run `oberth status` to inspect the current worktree")
}

// sessionMaintenanceInterval is how often live sessions are touched and dead
// ones pruned. It is a minute rather than the scan cadence because both are
// database writes and neither is urgent: the seven-day window has a minute of
// slack in it.
const sessionMaintenanceInterval = time.Minute

var sessionMaintenanceStop chan struct{}

// startSessionMaintenance keeps last_seen fresh for every live session and
// drops the ones nothing has started anything with for sessions.Retention
// (spec 2 §3: inactive sessions are kept 7 days, then pruned).
func startSessionMaintenance(rt *Runtime) {
	stopSessionMaintenance()
	if rt.Store == nil {
		return
	}
	stop := make(chan struct{})
	sessionMaintenanceStop = stop
	go func() {
		t := time.NewTicker(sessionMaintenanceInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				SweepSessions(rt, time.Now())
			}
		}
	}()
}

func stopSessionMaintenance() {
	if sessionMaintenanceStop != nil {
		close(sessionMaintenanceStop)
		sessionMaintenanceStop = nil
	}
}

// SweepSessions is one maintenance pass: touch the live sessions, prune the
// ones last seen more than sessions.Retention ago. Exported so a test can run
// a pass without waiting out the timer.
func SweepSessions(rt *Runtime, now time.Time) {
	if rt == nil || rt.Store == nil {
		return
	}
	table := rt.Store.Sessions()
	seen := map[string]bool{}
	for _, l := range liveSessions(rt) {
		if l.Session.ID == "" || seen[l.Session.ID] {
			continue
		}
		seen[l.Session.ID] = true
		if err := table.Touch(l.Session.ID, now); err != nil {
			rt.Logger.Warn("touching a live session", "session", l.Session.ID, "error", err)
		}
	}
	n, err := table.Prune(now.Add(-sessions.Retention))
	if err != nil {
		rt.Logger.Warn("pruning sessions", "error", err)
		return
	}
	if n > 0 {
		rt.Logger.Info("pruned inactive sessions", "count", n)
	}
}
