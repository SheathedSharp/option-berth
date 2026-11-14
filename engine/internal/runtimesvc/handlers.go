package runtimesvc

import (
	"context"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The core method set. Other packages own further namespaces and register
// those handlers from their own init(), so this list only ever grows sideways.
func init() {
	registerDaemonHandlers()
	registerStateHandlers()
	registerStreamHandlers()
}

func registerDaemonHandlers() {
	RegisterHandler("daemon.hello", handleHello)
	RegisterHandler("daemon.status", handleStatus)
	RegisterHandler("daemon.shutdown", handleShutdown)
	RegisterHandler("daemon.schema", handleSchema)
}

func registerStateHandlers() {
	RegisterHandler("state.snapshot", handleSnapshot)
	RegisterHandler("state.subscribe", handleSubscribe)
	RegisterHandler("state.unsubscribe", handleUnsubscribe)
}

func registerStreamHandlers() {
	RegisterHandler("stream.cancel", handleStreamCancel)
}

// parseInclude turns the wire's ["stats","health"] into the scanner's struct.
// An unknown entry is a client error, not a silent no-op, so a typo inside
// `include` surfaces immediately.
func parseInclude(in rpc.Include) (scanner.Include, error) {
	wanted := scanner.Include{}
	for _, item := range in {
		switch name := strings.ToLower(strings.TrimSpace(string(item))); name {
		case "":
			continue
		case "stats":
			wanted.Stats = true
		case "health":
			wanted.Health = true
		default:
			return wanted, rpc.NewError(rpc.CodeInvalidParams,
				"unknown include "+item, `include accepts "stats" and "health"`)
		}
	}
	return wanted, nil
}

func handleHello(ctx context.Context, req *Request) (any, error) {
	var p rpc.DaemonHelloParams
	err := req.Bind(&p)
	if err != nil {
		return nil, err
	}
	unnamed := p.Client == ""
	if unnamed {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "client is required",
			`send {"client": "cli", "client_version": "..."}`)
	}
	req.Conn.setHello(p.Client, p.ClientVersion, p.Keepalive)
	rt := req.Runtime
	rt.Logger.Debug("hello", "conn", req.Conn.ID(),
		"client", p.Client, "client_version", p.ClientVersion, "keepalive", p.Keepalive)

	return rpc.DaemonHelloResult{
		Capabilities:    Capabilities(),
		ProtocolVersion: rpc.ProtocolVersion,
		DaemonVersion:   rt.Version,
		PID:             rt.PID,
		StartedAt:       rt.StartedAt.Format(time.RFC3339),
		Socket:          rt.Socket,
		BinaryPath:      rt.BinaryPath,
		Keepalive:       p.Keepalive,
		Observation:     rt.RefreshObservation(ctx),
	}, nil
}

func handleStatus(ctx context.Context, req *Request) (any, error) {
	rt := req.Runtime
	status := rt.Scanner.Status()
	lastScan := ""
	if !status.LastScanAt.IsZero() {
		lastScan = status.LastScanAt.Format(time.RFC3339)
	}
	return rpc.DaemonStatusResult{
		Scans:              status.Scans,
		ScanIntervalMs:     status.IntervalMs,
		ScanBaseIntervalMs: status.BaseIntervalMs,
		StatsIntervalMs:    status.StatsIntervalMs,
		PID:                rt.PID,
		Uptime:             rt.Uptime().Round(time.Second).String(),
		Subscribers:        rt.Subscribers(),
		LastScanAt:         lastScan,
		Commit:             buildinfo.Commit,
		Built:              buildinfo.Date,
		DBPath:             rt.DBPath(),
		Observation:        rt.RefreshObservation(ctx),
	}, nil
}

func handleShutdown(_ context.Context, req *Request) (any, error) {
	req.Runtime.Logger.Info("shutdown requested by client", "conn", req.Conn.ID())
	// The dispatcher stops the daemon only after this reply has been queued, so
	// the client sees its {ok: true} before the socket goes away.
	req.Conn.requestShutdown()
	return rpc.OKResult{OK: true}, nil
}

func handleSchema(_ context.Context, _ *Request) (any, error) {
	schema := rpc.DaemonSchemaResult{Schema: rpc.Marshal()}
	return schema, nil
}

func handleSnapshot(_ context.Context, req *Request) (any, error) {
	var p rpc.StateSnapshotParams
	err := req.Bind(&p)
	if err != nil {
		return nil, err
	}
	include, err := parseInclude(p.Include)
	if err != nil {
		return nil, err
	}
	snapshot, err := req.Runtime.Scanner.SnapshotAll(include)
	if err != nil {
		return nil, scanFailure(err)
	}
	// Attach in-flight start attempts on a copy. The cached snapshot stays
	// attempt-free, so a delta never carries one and a later scan cannot
	// mistake the attempt for a run.
	snapshot.Groups = joinStartAttempts(req.Runtime, snapshot.Groups)
	scope := scopeOf(p.Scope)
	if !scope.Empty() {
		if scope.Workspace != "" && !scope.HasSelectors() && !req.Runtime.Server().workspaceAvailable(scope.Workspace) {
			return nil, rpc.NewError(rpc.CodeInvalidParams,
				"workspace has no registered worktrees or repositories",
				"subscribe once with --workspace and an explicit --worktree or --repository")
		}
		scope = req.Runtime.Server().effectiveScope(scope)
		snapshot.Ports = filterPorts(snapshot.Ports, include)
		return state.StreamSnapshotFor(snapshot, scope), nil
	}
	return filterSnapshot(snapshot, include, scope), nil
}

func handleSubscribe(_ context.Context, req *Request) (any, error) {
	var p rpc.StateSubscribeParams
	err := req.Bind(&p)
	if err != nil {
		return nil, err
	}
	include, err := parseInclude(p.Include)
	if err != nil {
		return nil, err
	}
	scope := scopeOf(p.Scope)
	if !scope.Empty() && !p.Events {
		return nil, rpc.NewError(rpc.CodeInvalidParams,
			"scoped subscriptions require events=true",
			"set events to true to receive state.changed notifications")
	}

	// Nothing is scanned here. subscribe answers from the cached snapshot and
	// wakes the loop, so a subscriber wanting stats or health is answered at
	// once and receives those fields in the delta the woken tick publishes.
	// Scanning first would hold the reply for a whole stats collection — seconds
	// on a busy machine — and the first snapshot would trail by a delta anyway
	// by the time it arrived.
	if err := req.Runtime.Server().subscribe(req.Conn, req.ID, include, p.Events, scope, p.AfterSeq); err != nil {
		return nil, err
	}
	return nil, ErrResponseSent
}

func scopeOf(scope *state.Scope) state.Scope {
	if scope == nil {
		return state.Scope{}
	}
	return *scope
}

func handleUnsubscribe(_ context.Context, req *Request) (any, error) {
	srv := req.Runtime.Server()
	srv.unsubscribe(req.Conn)
	return rpc.OKResult{OK: true}, nil
}

func handleStreamCancel(_ context.Context, req *Request) (any, error) {
	var p rpc.StreamCancel
	err := req.Bind(&p)
	if err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "id is required", "")
	}
	if !req.Conn.CancelStream(p.ID) {
		return nil, streamNotFound(p.ID)
	}
	return rpc.OKResult{OK: true}, nil
}

// streamNotFound is the reply for cancelling an id this connection never
// registered.
func streamNotFound(id string) error {
	return rpc.NewError(rpc.CodeNotFound,
		"no stream "+id+" on this connection",
		"streams end on their own with stream.end; cancelling twice is not an error worth retrying")
}
