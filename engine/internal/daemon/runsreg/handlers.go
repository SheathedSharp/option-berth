package runsreg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/sessions"
	"github.com/sheathedsharp/option-berth/internal/spawn"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// Default is the registry this daemon serves. One daemon, one registry: the
// handlers close over it and the OnStart hook publishes it to the runtime so
// the group resolver can attribute ports through it.
var Default = New()

// pruneInterval matches the scanner's base tick: a run whose process is gone
// disappears from `runs.list` about as fast as its port disappears from a
// snapshot.
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
	if p.PID <= 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "pid is required",
			`send {"pid": <pid>, "group": "...", "name": "..."}`)
	}
	cwd, err := checkCwd(p.Cwd, p.AllowOutsideHome)
	if err != nil {
		return nil, err
	}

	rec := Record{
		PID:   p.PID,
		PPID:  p.PPID,
		Group: strings.TrimSpace(p.Group),
		Name:  strings.TrimSpace(p.Name),
		Cmd:   p.Cmd,
		Cwd:   cwd,
	}
	if p.ID != nil {
		rec.ID = *p.ID
	}
	if rec.ID == "" {
		rec.ID = spawn.NewID()
	}
	if p.PortHint != nil {
		rec.PortHint = *p.PortHint
	}
	if t, err := time.Parse(time.RFC3339, p.StartedAt); err == nil {
		rec.StartedAt = t
	}
	if p.Session != nil && p.Session.ID != "" {
		rec.Session = *p.Session
	}
	rec.Origin = Origin(req)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rec = Default.Register(rec)
	rememberSession(req.Runtime, rec.Session)
	req.Runtime.Logger.Debug("run registered",
		"id", rec.ID, "pid", rec.PID, "group", rec.Group, "name", rec.Name)
	// The next scan should see the new run's ports, not wait out the backoff.
	req.Runtime.Scanner.Invalidate()
	return rpc.RunsRegisterResult{ID: rec.ID}, nil
}

func handleUnregister(ctx context.Context, req *daemon.Request) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var p rpc.RunsUnregisterParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if p.PID <= 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "pid is required", `send {"pid": <pid>}`)
	}
	// A caller that waited on the run says how it ended, and the run joins the
	// exit history; one that only lets go of it is simply forgotten.
	if p.ExitCode != nil {
		Default.Exited(p.PID, *p.ExitCode, p.Stopped)
	} else {
		Default.Unregister(p.PID)
	}
	req.Runtime.Scanner.Invalidate()
	// Unregistering a run nobody registered is not an error: `oberth start`
	// always cleans up, whether or not the daemon saw the registration.
	return rpc.OKResult{OK: true}, nil
}

func handleList(ctx context.Context, req *daemon.Request) (any, error) {
	snap, err := req.Runtime.Scanner.SnapshotAllContext(ctx, scanner.Include{})
	if err != nil {
		return nil, err
	}
	records := Default.List()
	rows := make([]rpc.RunRecord, 0, len(records))
	for _, rec := range records {
		rows = append(rows, row(rec, snap))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exits := Default.Exits()
	exited := make([]rpc.RunRecord, 0, len(exits))
	for _, e := range exits {
		exited = append(exited, exitRow(e))
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
	cwd, err := checkCwd(p.Cwd, p.AllowOutsideHome)
	if err != nil {
		return nil, err
	}

	res := spawn.Resolve(cwd, p.Argv, deref(p.Group), deref(p.Name))
	hint := 0
	if p.PortHint != nil {
		hint = *p.PortHint
	}

	// The daemon's own environment says nothing about the agent that called
	// it, so a session has to be sent: `runs.spawn {session}` when the caller
	// captured one, otherwise whatever the request's own env carries.
	session := state.Session{}
	if p.Session != nil {
		session = *p.Session
	}
	if session.ID == "" {
		if s, ok := sessions.DetectFromEnv(envSlice(p.Env), sessions.Options{}); ok {
			s.Worktree, s.Branch = sessions.GitContext(cwd)
			session = s
		}
	}

	h, err := Spawn(ctx, req.Runtime, spawn.Request{
		Argv:     p.Argv,
		Cwd:      cwd,
		Env:      mergeEnv(p.Env),
		Group:    res.Group,
		Name:     res.Name,
		PortHint: hint,
		Session:  session,
		Detach:   true,
	}, Meta{Origin: Origin(req)})
	if err != nil {
		return nil, err
	}

	return rpc.RunsSpawnResult{
		// A run has no ports yet at the moment it starts, so `affected` is
		// empty; the ports arrive in the next state.delta.
		MutationResult: rpc.MutationResult{OK: true, Affected: []string{}},
		RunID:          h.ID,
		PID:            h.PID,
		LogPath:        h.LogPath,
	}, nil
}

// Spawn starts a detached run, registers it and reaps it. It is the one spawn
// path the daemon has: `runs.spawn` calls it for a single command and
// `groups.start` calls it once per service, so a service started from the
// desktop is attributed exactly like one started from the CLI (contract §4).
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

	registry := Default
	record := registry.Register(Record{
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
	})
	rememberSession(rt, h.Session)
	rt.Logger.Info("spawned a run",
		"id", h.ID, "pid", h.PID, "group", h.Group, "name", h.Name, "log", h.LogPath)
	rt.Scanner.Wake()

	// Reap it: an unwaited child stays a zombie, and a zombie pid still looks
	// alive to every liveness test, so the run would never be pruned.
	go func(rt *daemon.Runtime, h *spawn.Handle) {
		code, err := h.Wait()
		e, _ := registry.ExitedRun(record, code, false)
		rt.Logger.Info("run exited",
			"id", h.ID, "pid", h.PID, "code", code, "reason", e.Reason, "error", err)
		rt.Scanner.Invalidate()
	}(rt, h)
	return h, nil
}

// rememberSession writes a session through to the store the moment a run
// carries one, so an agent's session survives a daemon restart and stays
// readable for the seven days after its last run exits.
func rememberSession(rt *daemon.Runtime, s state.Session) {
	if s.ID == "" || rt == nil || rt.Store == nil {
		return
	}
	now := time.Now()
	err := rt.Store.Sessions().Upsert(store.SessionRow{
		ID: s.ID, Tool: s.Tool, Label: s.Label,
		Worktree: s.Worktree, Branch: s.Branch, Detected: s.Detected,
		FirstSeen: now, LastSeen: now,
	})
	if err != nil {
		rt.Logger.Warn("recording an agent session", "session", s.ID, "error", err)
	}
}

// envSlice renders a wire env map as the KEY=VALUE slice detection reads.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// CheckCwd cleans a working directory and enforces the home-directory rule.
// `groups.start` reuses it so one place decides what the daemon may run.
func CheckCwd(cwd string, allowOutsideHome bool) (string, error) {
	return checkCwd(cwd, allowOutsideHome)
}

// row renders one run for `runs.list`, including the ports it currently holds
// and whether it is still coming up.
func row(rec Record, snap state.Snapshot) rpc.RunRecord {
	out := rpc.RunRecord{
		ID:         rec.ID,
		PID:        rec.PID,
		Group:      rec.Group,
		Name:       rec.Name,
		Cmd:        rec.Cmd,
		Cwd:        rec.Cwd,
		StartedAt:  rec.StartedAt.Format(time.RFC3339),
		Ports:      portsOf(rec, snap),
		Status:     "running",
		ConfigPath: rec.ConfigPath,
		StartID:    rec.StartID,
		Origin:     rec.Origin,
		SpecHash:   rec.SpecHash,
		LogPath:    rec.LogPath,
	}
	if rec.PortHint > 0 {
		hint := rec.PortHint
		out.PortHint = &hint
		out.URL = serviceURL(hint)
		if !contains(out.Ports, hint) {
			// The expected port is not listening yet: the desktop and
			// `daemon.status` show the run as coming up rather than missing.
			out.Status = "starting"
		}
	}
	return out
}

// portsOf collects the ports a run owns from the last snapshot.
func portsOf(rec Record, snap state.Snapshot) []int {
	out := []int{}
	seen := map[int]bool{}
	for i := range snap.Ports {
		p := snap.Ports[i]
		if p.Run == nil {
			continue
		}
		if p.Run.RootPID != rec.PID && (rec.ID == "" || p.Run.ID != rec.ID) {
			continue
		}
		if !seen[p.Port] {
			seen[p.Port] = true
			out = append(out, p.Port)
		}
	}
	return out
}

// snapshot reads the daemon's current state without forcing a scan when the
// cache is warm.
func snapshot(rt *daemon.Runtime) state.Snapshot {
	snap, err := rt.Scanner.Snapshot(scanner.Include{})
	if err != nil {
		return rt.Scanner.Cached()
	}
	return snap
}

// checkCwd cleans a client-supplied working directory and enforces the spec's
// home-directory rule: the daemon refuses to touch anything outside the user's
// home unless the caller explicitly opts in (daemon spec, "Transport details").
func checkCwd(cwd string, allowOutsideHome bool) (string, error) {
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
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if allowOutsideHome {
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return abs, nil
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	if !underHome(abs, home) {
		return "", rpc.NewError(rpc.CodeOutsideHome,
			fmt.Sprintf("%s is outside %s", abs, home),
			"pass allow_outside_home: true to start commands outside your home directory")
	}
	return abs, nil
}

// underHome reports whether path is home or inside it.
func underHome(path, home string) bool {
	if path == home {
		return true
	}
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// mergeEnv layers a client's environment overrides onto the daemon's own.
func mergeEnv(over map[string]string) []string {
	if len(over) == 0 {
		return nil
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+len(over))
	for _, kv := range base {
		if key, _, ok := strings.Cut(kv, "="); ok {
			if _, replaced := over[key]; replaced {
				continue
			}
		}
		out = append(out, kv)
	}
	for k, v := range over {
		out = append(out, k+"="+v)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
