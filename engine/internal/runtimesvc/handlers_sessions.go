package runtimesvc

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// Session attribution reaches the runtime service through three doors: the scanner's
// snapshot collection, the `sessions.kill` RPC, and the periodic sweep that
// keeps recorded sessions inside their retention window. All three read the
// same two sources — the live run registry and the store — so a session can
// never be active in one place and forgotten in another.
func init() {
	RegisterHandler("sessions.kill", handleSessionsKill)
	RegisterCapability("sessions")

	OnStart(func(rt *Runtime) {
		rt.Scanner.SetSessions(func(ports []state.Port) []state.SessionRecord {
			return sessionFacts{rt: rt}.records(ports)
		})
		sessionSweep.begin(rt)
	})
	OnShutdown(func(bool) { sessionSweep.halt() })
}

// sessionRegistry is the optional half of the run registry that knows which
// agent started each live run. It is an interface here because the registry
// implementation imports this package, never the reverse.
type sessionRegistry interface {
	SessionRuns() []sessions.Live
}

// sessionFacts joins the runtime service's two session sources for the domain package.
type sessionFacts struct {
	rt *Runtime
}

// live is every run that still carries a session.
func (f sessionFacts) live() []sessions.Live {
	registry, ok := f.rt.Runs().(sessionRegistry)
	if !ok {
		return nil
	}
	return registry.SessionRuns()
}

// known is what the store remembers, live or not. A store failure degrades to
// "nothing remembered" and is stated once: the live registry still answers.
func (f sessionFacts) known() []sessions.Known {
	if f.rt.Store == nil {
		return nil
	}
	rows, err := f.rt.Store.Sessions().List()
	if err != nil {
		f.rt.Logger.Warn("reading recorded sessions", "error", err)
		return nil
	}
	out := make([]sessions.Known, len(rows))
	for i, row := range rows {
		out[i] = sessions.Known{
			Session:   sessionFromRow(row),
			FirstSeen: row.FirstSeen,
			LastSeen:  row.LastSeen,
		}
	}
	return out
}

// records is the `sessions` collection for one scan tick.
func (f sessionFacts) records(ports []state.Port) []state.SessionRecord {
	return sessions.Records(f.known(), f.live(), ports)
}

func sessionFromRow(row store.SessionRow) state.Session {
	return state.Session{
		ID:       row.ID,
		Tool:     row.Tool,
		Label:    row.Label,
		Worktree: row.Worktree,
		Branch:   row.Branch,
		Detected: row.Detected,
	}
}

// handleSessionsKill stops everything one session started. It returns the
// same envelope as a group kill: every listening port stamped with the
// session, plus the pid of any of its runs that has not opened a port yet, so
// "stop this session" also reaches a server still starting.
func handleSessionsKill(ctx context.Context, req *Request) (any, error) {
	var params rpc.SessionsKillParams
	if err := req.Bind(&params); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(params.ID)
	if id == "" {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "id is required",
			`send {"id": "claude-code:abc123"}`)
	}

	snapshot, err := killSnapshot(ctx, req)
	if err != nil {
		return nil, err
	}
	facts := sessionFacts{rt: req.Runtime}
	live := sessions.RunsOf(facts.live(), id)
	targets := sessionTargets(snapshot, live, id, req.Runtime.Runs().RunStart)
	if len(targets) == 0 {
		if _, known := findSession(facts.records(snapshot.Ports), id); !known {
			return nil, sessionNotFound(id)
		}
		// A recorded session with nothing running: stopping it succeeded
		// trivially, so this is an empty envelope rather than an error.
		return killEnvelope(nil), nil
	}

	options := killer.Options{
		Tree:   params.Tree,
		Force:  params.Force,
		DryRun: params.DryRun,
		Ports:  killerRows(snapshot),
	}
	options.OnSignal = stopSignalObserver(req.Runtime, snapshot, targets, options.DryRun, "")
	results := killer.KillPorts(ctx, targets, options)
	afterKill(req, options.DryRun)
	return killEnvelope(results), nil
}

// sessionTargets turns a session into kill targets. A port carries its bind
// address; a run that owns no port carries its pid and, when the registry
// remembers it, the start time the killer verifies before signalling.
func sessionTargets(snapshot state.Snapshot, live []sessions.Live, id string, startOf func(int) (time.Time, bool)) []killer.Target {
	targets := make([]killer.Target, 0, len(live))
	onPort := make(map[int]bool, len(snapshot.Ports))
	for i := range snapshot.Ports {
		port := snapshot.Ports[i]
		if port.Session == nil || port.Session.ID != id {
			continue
		}
		targets = append(targets, killer.Target{Port: port.Port, BindAddress: port.BindAddress})
		if port.Run != nil {
			onPort[port.Run.RootPID] = true
		}
	}
	for _, run := range live {
		if run.PID <= 0 || onPort[run.PID] {
			continue
		}
		target := killer.Target{PID: run.PID}
		if startOf != nil {
			if startedAt, known := startOf(run.PID); known {
				target.StartedAt = startedAt
			}
		}
		targets = append(targets, target)
	}
	return targets
}

// findSession matches a full id first, then a unique prefix, which is what
// makes a long id usable by hand. An ambiguous prefix is refused rather than
// guessed.
func findSession(records []state.SessionRecord, id string) (state.SessionRecord, bool) {
	var candidate state.SessionRecord
	matches := 0
	for _, record := range records {
		if record.ID == id {
			return record, true
		}
		if strings.HasPrefix(record.ID, id) {
			candidate = record
			matches++
		}
	}
	return candidate, matches == 1
}

func sessionNotFound(id string) error {
	return rpc.NewError(rpc.CodeSessionNotFound, "no session "+id,
		"run `oberth status` to inspect the current worktree")
}

// sessionSweepInterval is one minute: both maintenance writes are cheap and
// neither is urgent, and the retention window has a minute of slack in it.
const sessionSweepInterval = time.Minute

// sessionMaintenance owns the runtime service's only session sweep goroutine. It holds
// the stop channel behind a mutex so a restart cannot leave two sweeps
// touching the same table.
type sessionMaintenance struct {
	mu   sync.Mutex
	stop chan struct{}
}

var sessionSweep sessionMaintenance

func (m *sessionMaintenance) begin(rt *Runtime) {
	m.halt()
	if rt.Store == nil {
		return
	}
	stop := make(chan struct{})
	m.mu.Lock()
	m.stop = stop
	m.mu.Unlock()
	go m.run(rt, stop)
}

func (m *sessionMaintenance) halt() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stop == nil {
		return
	}
	close(m.stop)
	m.stop = nil
}

func (m *sessionMaintenance) run(rt *Runtime, stop chan struct{}) {
	ticker := time.NewTicker(sessionSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			SweepSessions(rt, time.Now())
		}
	}
}

// SweepSessions runs one maintenance pass: touch every live session and prune
// the ones last seen longer than sessions.Retention ago. It is exported so a
// test can run a pass without waiting for the ticker.
func SweepSessions(rt *Runtime, now time.Time) {
	if rt == nil || rt.Store == nil {
		return
	}
	table := rt.Store.Sessions()
	touched := make(map[string]bool)
	for _, run := range (sessionFacts{rt: rt}).live() {
		id := run.Session.ID
		if id == "" || touched[id] {
			continue
		}
		touched[id] = true
		if err := table.Touch(id, now); err != nil {
			rt.Logger.Warn("touching a live session", "session", id, "error", err)
		}
	}
	removed, err := table.Prune(now.Add(-sessions.Retention))
	if err != nil {
		rt.Logger.Warn("pruning sessions", "error", err)
		return
	}
	if removed > 0 {
		rt.Logger.Info("pruned inactive sessions", "count", removed)
	}
}
