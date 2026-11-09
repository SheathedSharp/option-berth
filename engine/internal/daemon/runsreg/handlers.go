package runsreg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/spawn"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// Handlers for the `runs.*` namespace. A run is one process option-berth
// started; the registry is where it lives, and these four methods are the only
// way a client adds one, ends one or reads them back.

// Default is the registry this daemon serves. One daemon, one registry: the
// handlers close over it, and the startup hook publishes it to the runtime so
// port attribution and the group resolver read the same records.
var Default = New()

// pruneInterval is the scanner's base tick. Pruning a dead run on the cadence
// the ports are scanned at keeps `runs.list` and the port snapshot telling the
// same story about the same process.
const pruneInterval = scanner.BaseInterval

func init() {
	daemon.RegisterHandler("runs.register", handleRegister)
	daemon.RegisterHandler("runs.unregister", handleUnregister)
	daemon.RegisterHandler("runs.list", handleList)
	daemon.RegisterHandler("runs.spawn", handleSpawn)
	daemon.RegisterCapability("runs")
	daemon.OnGroupRename(func(renames map[string]string) { Default.RenameGroups(renames) })

	daemon.OnStartChecked(func(rt *daemon.Runtime) error {
		Default.SetLogger(rt.Logger)
		// Both recovery stages are required before installing the registry or
		// starting its worker. A failed LoadExits must not leave history nil
		// while the daemon accepts writes it can no longer persist.
		n, err := restoreRequiredRegistry(Default, rt.Store)
		if err != nil {
			return err
		}
		if n > 0 {
			rt.Logger.Info("imported runs.json into the run registry", "runs", n)
		}
		rt.SetRuns(Default)
		Default.PruneCodeSnapshots()
		startPruning(rt)
		return nil
	})
	daemon.OnShutdown(func(bool) { stopPruning() })
}

// registryStartupRecovery is the existing registry's startup work, kept small
// so failure ordering can be checked without starting a daemon or touching a
// real user's runs.json. It is not a second registry or a runtime fallback.
type registryStartupRecovery interface {
	LoadExits(*store.Store) error
	ImportLegacy() (int, error)
}

// restoreRequiredRegistry completes required recovery before publication.
// Nil store retains the existing storeless mode; an actual read failure does
// not select that mode. Preserve the cause for diagnostics and do not import
// or rewrite runs.json after an exit-history failure. The enclosing checked
// startup hook returns the error to the daemon's existing cleanup path.
func restoreRequiredRegistry(r registryStartupRecovery, st *store.Store) (int, error) {
	if err := r.LoadExits(st); err != nil {
		return 0, fmt.Errorf("restore run exit history: %w", err)
	}
	n, err := r.ImportLegacy()
	if err != nil {
		return 0, fmt.Errorf("restore live run registry: %w", err)
	}
	return n, nil
}

func handleRegister(ctx context.Context, req *daemon.Request) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var p rpc.RunsRegisterParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if err := requirePID(p.PID, `send {"pid": <pid>, "group": "...", "name": "..."}`); err != nil {
		return nil, err
	}
	cwd, err := CheckCwd(p.Cwd, p.AllowOutsideHome)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rec := Default.Register(runFromWire(p, cwd, Origin(req)))
	rememberSession(req.Runtime, rec.Session)
	req.Runtime.Logger.Debug("run registered",
		"id", rec.ID, "pid", rec.PID, "group", rec.Group, "name", rec.Name)
	// The next scan should see the new run's ports, not wait out the backoff.
	req.Runtime.Scanner.Invalidate()
	return rpc.RunsRegisterResult{ID: rec.ID}, nil
}

// runFromWire is the record a `runs.register` call describes. The caller says
// which process it started and where; everything else here is a default, so
// this is a pure function of the request and the two facts that were already
// checked (the working directory and the requesting client).
//
// A run the caller does not name gets an id here. That id is what makes a
// recycled pid still attributable: the registry matches a port's run by id
// before it falls back to the pid.
func runFromWire(p rpc.RunsRegisterParams, cwd, origin string) Record {
	rec := Record{
		ID:       textOrEmpty(p.ID),
		PID:      p.PID,
		PPID:     p.PPID,
		Group:    strings.TrimSpace(p.Group),
		Name:     strings.TrimSpace(p.Name),
		Cmd:      p.Cmd,
		Cwd:      cwd,
		PortHint: portOrZero(p.PortHint),
		Origin:   origin,
	}
	if rec.ID == "" {
		rec.ID = spawn.NewID()
	}
	// A start time the caller cannot spell in RFC3339 is not a reason to refuse
	// the registration: the registry fills in the moment it recorded the run.
	if started, err := time.Parse(time.RFC3339, p.StartedAt); err == nil {
		rec.StartedAt = started
	}
	// An id-less session is not a session: it names no agent, so recording it
	// would stamp ports with an identity nothing can look up.
	if p.Session != nil && p.Session.ID != "" {
		rec.Session = *p.Session
	}
	return rec
}

func handleUnregister(ctx context.Context, req *daemon.Request) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var p rpc.RunsUnregisterParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if err := requirePID(p.PID, `send {"pid": <pid>}`); err != nil {
		return nil, err
	}
	// How the run ended is the caller's evidence: a caller that waited on the
	// process says so and the run becomes exit history, while one that is only
	// letting go of it leaves no outcome behind.
	if p.ExitCode != nil {
		Default.Exited(p.PID, *p.ExitCode, p.Stopped)
	} else {
		Default.Unregister(p.PID)
	}
	req.Runtime.Scanner.Invalidate()
	// Forgetting a run nobody registered is not an error: `oberth start` always
	// cleans up after itself, whether or not the daemon saw the registration.
	return rpc.OKResult{OK: true}, nil
}

func handleList(ctx context.Context, req *daemon.Request) (any, error) {
	// A run's identity and log path do not come from the listener table. When
	// observation is blind, the ports are unknown — not the runs — so list what
	// the registry knows and leave the per-run port list empty. Failing here
	// would make `logs --run` depend on a collector it never reads.
	snap, err := req.Runtime.Scanner.SnapshotAllContext(ctx, scanner.Include{})
	portsKnown := err == nil
	if err != nil {
		snap = state.Snapshot{}
	}
	records := Default.List()
	rows := make([]rpc.RunRecord, 0, len(records))
	for _, rec := range records {
		rows = append(rows, row(rec, snap, portsKnown))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exits := Default.Exits()
	exited := make([]rpc.RunRecord, 0, len(exits))
	for _, e := range exits {
		exited = append(exited, exitRow(e, snap.Ports))
	}
	return rpc.RunsListResult{Runs: rows, Exited: exited}, nil
}

func handleSpawn(ctx context.Context, req *daemon.Request) (any, error) {
	var p rpc.RunsSpawnParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if len(p.Argv) == 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "argv is required",
			`send {"argv": ["npm", "run", "dev"], "cwd": "/path/to/repo"}`)
	}
	cwd, err := CheckCwd(p.Cwd, p.AllowOutsideHome)
	if err != nil {
		return nil, err
	}

	res := spawn.Resolve(cwd, p.Argv, textOrEmpty(p.Group), textOrEmpty(p.Name))
	h, err := Spawn(ctx, req.Runtime, spawn.Request{
		Argv:     p.Argv,
		Cwd:      cwd,
		Env:      mergedEnv(p.Env),
		Group:    res.Group,
		Name:     res.Name,
		PortHint: portOrZero(p.PortHint),
		Session:  sessionOf(p, cwd),
		Detach:   true,
	}, Meta{Origin: Origin(req)})
	if err != nil {
		return nil, err
	}

	return rpc.RunsSpawnResult{
		// A run has no ports at the moment it starts, so `affected` is empty and
		// the ports arrive with the next state.delta.
		MutationResult: rpc.MutationResult{OK: true, Affected: []string{}},
		RunID:          h.ID,
		PID:            h.PID,
		LogPath:        h.LogPath,
	}, nil
}

// sessionOf is the agent session a spawned run belongs to: the one the caller
// sent, or — when it sent none — the one the request's own environment names.
//
// The daemon's environment says nothing about the agent that called it, which
// is why the session has to come over the wire at all; the env fallback exists
// only for a caller that forwarded its environment instead of capturing one.
func sessionOf(p rpc.RunsSpawnParams, cwd string) state.Session {
	if p.Session != nil {
		return *p.Session
	}
	detected, ok := sessions.DetectFromEnv(envPairs(p.Env), sessions.Options{})
	if !ok {
		return state.Session{}
	}
	detected.Worktree, detected.Branch = sessions.GitContext(cwd)
	return detected
}

// Spawn starts a detached run, registers it and reaps it. It is the one spawn
// path the daemon has: `runs.spawn` calls it for a single command and
// `groups.start` calls it once per service, so a service started from the
// desktop is attributed exactly like one started from the CLI.
//
// The caller has already resolved the group, the name and the working
// directory; CheckCwd is the home-directory guard.
func Spawn(ctx context.Context, rt *daemon.Runtime, req spawn.Request, meta Meta) (*spawn.Handle, error) {
	req.Detach = true
	h, err := spawn.Spawn(ctx, req)
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInternal, err.Error(),
			"check the command and its working directory")
	}

	record := Default.Register(runFromHandle(h, meta))
	rememberSession(rt, h.Session)
	rt.Logger.Info("spawned a run",
		"id", h.ID, "pid", h.PID, "group", h.Group, "name", h.Name, "log", h.LogPath)
	rt.Scanner.Wake()

	// Reap it: an unwaited child stays a zombie, and a zombie pid still looks
	// alive to every liveness test, so the run would never be pruned.
	go func(rt *daemon.Runtime, h *spawn.Handle) {
		code, err := h.Wait()
		e, _ := Default.ExitedRun(record, code, false)
		rt.Logger.Info("run exited",
			"id", h.ID, "pid", h.PID, "code", code, "reason", e.Reason, "error", err)
		rt.Scanner.Invalidate()
	}(rt, h)
	return h, nil
}

// runFromHandle is the record a freshly started process becomes: the launch
// facts the spawner observed, plus what the caller knew about why it started —
// the manifest it came from, the start that grouped it, and the client that
// asked.
func runFromHandle(h *spawn.Handle, meta Meta) Record {
	return Record{
		ID:         h.ID,
		PID:        h.PID,
		PPID:       h.PPID,
		Group:      h.Group,
		Name:       h.Name,
		Cmd:        h.Cmd,
		Cwd:        h.Cwd,
		PortHint:   h.PortHint,
		StartedAt:  h.StartedAt,
		Session:    h.Session,
		ConfigPath: meta.ConfigPath,
		StartID:    meta.StartID,
		Origin:     meta.Origin,
		SpecHash:   meta.SpecHash,
		LogPath:    h.LogPath,
		LogOffset:  h.LogOffset,
	}
}

// rememberSession writes a session through to the store the moment a run
// carries one, so an agent's session survives a daemon restart and stays
// readable for the seven days after its last run exits.
func rememberSession(rt *daemon.Runtime, s state.Session) {
	if s.ID == "" || rt == nil || rt.Store == nil {
		return
	}
	// first_seen and last_seen are left zero on purpose: the store reads zero
	// as "now" and, on an id it already holds, writes only last_seen and the
	// descriptive fields. Deciding the timestamp here would duplicate that
	// rule in a second place, and the obvious spelling — this moment for both
	// — is the one that would wipe a session's first appearance.
	row := store.SessionRow{
		ID: s.ID, Tool: s.Tool, Label: s.Label,
		Worktree: s.Worktree, Branch: s.Branch, Detected: s.Detected,
	}
	if err := rt.Store.Sessions().Upsert(row); err != nil {
		rt.Logger.Warn("recording an agent session", "session", s.ID, "error", err)
	}
}

// envPairs renders a wire env map as the KEY=VALUE slice session detection
// reads, which is the shape an environment has inside a process.
func envPairs(env map[string]string) []string {
	pairs := make([]string, 0, len(env))
	for key, value := range env {
		pairs = append(pairs, key+"="+value)
	}
	return pairs
}

// mergedEnv is the daemon's own environment with the caller's entries taking
// the place of the keys they name. Nothing is merged when the caller sent no
// overrides: the child then inherits the daemon's environment as it is.
func mergedEnv(over map[string]string) []string {
	if len(over) == 0 {
		return nil
	}
	base := os.Environ()
	merged := make([]string, 0, len(base)+len(over))
	for _, pair := range base {
		key, _, _ := strings.Cut(pair, "=")
		if _, replaced := over[key]; replaced {
			continue
		}
		merged = append(merged, pair)
	}
	for key, value := range over {
		merged = append(merged, key+"="+value)
	}
	return merged
}

// CheckCwd cleans a working directory and enforces the home-directory rule.
// `groups.start` reuses it so one place decides what the daemon may run.
func CheckCwd(cwd string, allowOutsideHome bool) (string, error) {
	abs, err := absoluteCwd(cwd)
	if err != nil {
		return "", err
	}
	if allowOutsideHome {
		return abs, nil
	}
	home, resolved := homeDir()
	if !resolved {
		// No home to compare against means the rule cannot be applied. Refusing
		// every directory then would break a legitimate install rather than
		// protect it.
		return abs, nil
	}
	if groups.Under(abs, home) {
		return abs, nil
	}
	return "", rpc.NewError(rpc.CodeOutsideHome,
		fmt.Sprintf("%s is outside %s", abs, home),
		"pass allow_outside_home: true to start commands outside your home directory")
}

// absoluteCwd resolves a client-supplied path to one real absolute directory,
// using the daemon's own working directory when the caller named none. Both
// spellings of one directory — through a symlink or not — come out the same,
// so the home check cannot pass one and fail the other.
func absoluteCwd(cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", rpc.NewError(rpc.CodeInvalidParams, "cwd is required", "")
		}
		cwd = wd
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", rpc.NewError(rpc.CodeInvalidParams, "cwd is not a usable path: "+cwd, "")
	}
	return groups.Canonical(abs), nil
}

// homeDir is the user's home with any symlinks resolved, and whether one could
// be resolved at all.
func homeDir() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		return resolved, true
	}
	return home, true
}

// row renders one live run for `runs.list`: what the registry knows about it,
// the ports it currently holds, and whether it is still coming up.
//
// portsKnown separates "the run has no port" from "the listener table could not
// be read". Without it, a blind collector would make every port-hinted run look
// like it was still starting.
func row(rec Record, snap state.Snapshot, portsKnown bool) rpc.RunRecord {
	out := liveRow(rec)
	out.Ports = portsOf(rec, snap)
	out.Status = runStatus(rec, out.Ports, portsKnown)
	if rec.PortHint > 0 {
		// The desktop and `daemon.status` key off the hint: it is the one thing
		// a run can say about itself before any scan sees it listening.
		hint := rec.PortHint
		out.PortHint = &hint
		out.URL = serviceURL(hint)
	}
	return out
}

// liveRow is the part of a row that comes straight from the run's own record.
// A run that has ended carries the same block, which is why exitRow starts here
// too: identity, provenance and where it logs are the run's facts either way.
func liveRow(rec Record) rpc.RunRecord {
	return rpc.RunRecord{
		ID:         rec.ID,
		PID:        rec.PID,
		Group:      rec.Group,
		Name:       rec.Name,
		Cmd:        rec.Cmd,
		Cwd:        rec.Cwd,
		StartedAt:  rec.StartedAt.Format(time.RFC3339),
		ConfigPath: rec.ConfigPath,
		StartID:    rec.StartID,
		Origin:     rec.Origin,
		SpecHash:   rec.SpecHash,
		LogPath:    rec.LogPath,
		LogOffset:  &rec.LogOffset,
	}
}

// runStatus is "starting" while a port-hinted run has not bound its port and
// the listener table was readable, and "running" in every other case.
func runStatus(rec Record, ports []int, portsKnown bool) string {
	if rec.PortHint > 0 && portsKnown && !slices.Contains(ports, rec.PortHint) {
		return "starting"
	}
	return "running"
}

// portsOf collects the ports a run owns from the last snapshot. A row belongs
// to the run when it names the same root pid, or — for a run that has an id —
// the same run id, which survives a pid the OS recycled.
func portsOf(rec Record, snap state.Snapshot) []int {
	ports := []int{}
	seen := map[int]bool{}
	for i := range snap.Ports {
		owner := snap.Ports[i].Run
		if owner == nil || !ownedBy(*owner, rec) {
			continue
		}
		if seen[snap.Ports[i].Port] {
			continue
		}
		seen[snap.Ports[i].Port] = true
		ports = append(ports, snap.Ports[i].Port)
	}
	return ports
}

// ownedBy reports whether a snapshot row's run reference names this record. A
// row that carries no run id can still be matched by its root pid, which is the
// only evidence an older caller publishes.
func ownedBy(ref state.Run, rec Record) bool {
	if ref.RootPID == rec.PID {
		return true
	}
	return rec.ID != "" && ref.ID == rec.ID
}

// requirePID is the one place that decides a pid-less run call is invalid. It
// carries the caller's own example shape, so the refusal shows a request they
// can paste back rather than the registry's field names.
func requirePID(pid int, example string) error {
	if pid <= 0 {
		return rpc.NewError(rpc.CodeInvalidParams, "pid is required", example)
	}
	return nil
}

// valueOrZero reads an optional wire value, returning the type's zero when the
// caller named none. The optional-field helpers below differ only in their
// element type, so the nil rule lives here once instead of once per type.
func valueOrZero[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}

// textOrEmpty reads an optional wire string.
func textOrEmpty(s *string) string { return valueOrZero(s) }

// portOrZero reads an optional port hint; zero means the caller named none.
func portOrZero(p *int) int { return valueOrZero(p) }
