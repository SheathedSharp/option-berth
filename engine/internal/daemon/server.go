package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// DefaultIdleTimeout is the config default for daemon.idle_timeout. Zero means
// "never idle out".
const DefaultIdleTimeout = 30 * time.Minute

// idleCheckInterval bounds how often the idle watchdog wakes.
const idleCheckInterval = 5 * time.Second

// stateReplayCapacity bounds the in-memory resume window. A cursor older than
// this window safely falls back to a fresh snapshot; the daemon never pretends
// to replay a partial stream.
const stateReplayCapacity = 256

// Capabilities lists the method families this build serves. Clients read it
// from daemon.hello to tell whether, say, share.* exists before calling it.
// Later steps append their own family as they land.
func Capabilities() []string {
	return append([]string{"state", rpc.CapabilityStateScope, "ports.read", "ports.kill", "store", "streams"},
		registeredCapabilities()...)
}

// Options configures a Server.
type Options struct {
	// Socket is the address to listen on. Empty means SocketPath().
	Socket string
	// Version is the daemon's own version string, reported by daemon.hello.
	Version string
	// BinaryPath is the executable clients should re-exec to autostart us.
	BinaryPath string
	// IdleTimeout stops the daemon after this long with no clients, no
	// subscribers and no keepalive. Zero disables it.
	IdleTimeout time.Duration
	// StatsInterval is the cadence of the scanner's stats-only tick
	// (`daemon.stats_interval`). Zero means scanner.StatsInterval. Ignored
	// when Scanner is set, since the caller built the loop itself.
	StatsInterval time.Duration
	// ScanInterval is the base cadence of the scanner's port scan
	// (`daemon.scan_interval`). Zero means scanner.BaseInterval. Ignored when
	// Scanner is set, for the same reason StatsInterval is.
	ScanInterval time.Duration
	// Logger receives the daemon's structured log. Required in production;
	// tests may leave it nil for a discard logger.
	Logger *slog.Logger
	// Lock is an already-acquired single-instance lock. When nil, Serve takes
	// one itself and releases it on exit.
	Lock *Lock
	// Scanner overrides the scan loop. Tests inject one; production leaves it
	// nil and gets the OS scanner.
	Scanner *scanner.Loop
	// DBPath is the SQLite database to open. Empty means store.Path(); tests
	// point it at a temp directory.
	DBPath string
}

// Server is the daemon: a listener, a set of connections, the subscription
// fan-out and the process lifecycle.
type Server struct {
	opts    Options
	logger  *slog.Logger
	socket  string
	ln      net.Listener
	runtime *Runtime
	loop    *scanner.Loop

	lock    *Lock
	ownLock bool

	nextConnID         atomic.Uint64
	nextWorkspaceEvent atomic.Uint64
	keepalives         atomic.Int64
	lastActive         atomic.Int64 // unix nanos

	subsMu sync.RWMutex
	conns  map[uint64]*Conn
	// workspaceScopes is an ephemeral broker relation. Each connection may
	// contribute explicit worktrees/repositories to a named workspace; no
	// relation is persisted in oberth.yaml or the database.
	workspaceScopes map[string]map[uint64]state.Scope

	replayMu sync.Mutex
	replay   []stateReplay
	// Oldest physical slot; logical replay order is reconstructed only on read.
	replayStart int

	pendingMu sync.Mutex
	pending   []state.Event

	stopOnce sync.Once
	stopping chan struct{}
	done     chan struct{}
	graceful atomic.Bool
	wg       sync.WaitGroup
}

type stateReplay struct {
	prev   state.Snapshot
	next   state.Snapshot
	events []state.Event
}

// New builds a Server. It does not listen; call Serve.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Socket == "" {
		opts.Socket = SocketPath()
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	s := &Server{
		opts:            opts,
		logger:          opts.Logger,
		socket:          opts.Socket,
		conns:           map[uint64]*Conn{},
		workspaceScopes: map[string]map[uint64]state.Scope{},
		stopping:        make(chan struct{}),
		done:            make(chan struct{}),
	}
	s.touch()

	s.loop = opts.Scanner
	if s.loop == nil {
		s.loop = scanner.New(scanner.Options{
			DaemonVersion: opts.Version,
			Logger:        opts.Logger,
			StatsInterval: opts.StatsInterval,
			ScanInterval:  opts.ScanInterval,
		})
	}
	s.loop.SetDemand(s.demand)
	s.loop.SetPublisher(s.publish)

	binary := opts.BinaryPath
	if binary == "" {
		binary, _ = os.Executable()
	}
	s.runtime = &Runtime{
		Version:    opts.Version,
		Socket:     s.socket,
		BinaryPath: binary,
		PID:        os.Getpid(),
		StartedAt:  time.Now(),
		Logger:     opts.Logger,
		Scanner:    s.loop,
		srv:        s,
	}
	s.loop.SetRuns(s.runtime.RunRegistry)
	return s
}

// Runtime is the runtime handed to handlers and extension hooks.
func (s *Server) Runtime() *Runtime { return s.runtime }

// Socket is the address the server listens on.
func (s *Server) Socket() string { return s.socket }

// Serve takes the single-instance lock, listens, and accepts connections until
// ctx is cancelled or Shutdown is called. It returns ErrAlreadyRunning when
// another daemon holds the lock.
func (s *Server) Serve(ctx context.Context) error {
	defer close(s.done)

	if s.opts.Lock != nil {
		s.lock = s.opts.Lock
	} else {
		lock, err := AcquireLock(lockPathFor(s.socket))
		if err != nil {
			return err
		}
		s.lock, s.ownLock = lock, true
	}
	defer func() {
		if s.ownLock {
			_ = s.lock.Release()
		}
	}()

	// The lock is ours, so any socket file still on disk belongs to a daemon
	// that died without cleaning up.
	if err := removeStaleSocket(s.socket); err != nil {
		return fmt.Errorf("removing stale socket %s: %w", s.socket, err)
	}

	ln, err := listen(s.socket)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.socket, err)
	}
	s.ln = ln

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The store comes up before the scanner so the first tick already resolves
	// groups with the stored pins and publishes the stored names.
	s.openStore()
	defer s.closeStore()

	// Restore required ownership before starting the scanner or accepting RPC.
	// Optional hooks retain their existing adapter, but failed required work
	// must not be presented as an empty successful daemon.
	if err := runStartHooks(s.runtime); err != nil {
		_ = ln.Close()
		runShutdownHooks(false)
		_ = removeStaleSocket(s.socket)
		return fmt.Errorf("starting daemon: %w", err)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop.Run(ctx)
	}()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.watchIdle(ctx)
	}()

	s.logger.Info("daemon listening",
		"socket", s.socket, "pid", s.runtime.PID, "version", s.opts.Version,
		"idle_timeout", s.opts.IdleTimeout.String())

	go func() {
		select {
		case <-ctx.Done():
			s.Shutdown()
		case <-s.stopping:
		}
	}()

	acceptErr := s.accept(ctx)

	cancel()
	s.closeAllConns("daemon stopping")
	s.wg.Wait()
	runShutdownHooks(s.graceful.Load())
	_ = removeStaleSocket(s.socket)
	s.logger.Info("daemon stopped", "graceful", s.graceful.Load())
	return acceptErr
}

// accept is the accept loop. A failure after Shutdown has been requested is
// the listener closing under us, not an error.
func (s *Server) accept(ctx context.Context) error {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.stopping:
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		s.startConn(ctx, nc)
	}
}

// startConn registers a connection and starts its read and write loops.
func (s *Server) startConn(ctx context.Context, nc net.Conn) {
	c := newConn(s.nextConnID.Add(1), s, nc)

	s.subsMu.Lock()
	s.conns[c.id] = c
	s.subsMu.Unlock()
	s.touch()
	s.logger.Debug("client connected", "conn", c.id)

	c.writerWG.Add(1)
	go c.writeLoop()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer c.Close()
		c.readLoop(ctx)
	}()
}

// removeConn drops a connection from the registry. Called once per connection,
// from Conn.closeWithReason.
func (s *Server) removeConn(c *Conn, reason string) {
	s.subsMu.Lock()
	workspace, workspaceChanged := s.detachWorkspaceLocked(c)
	delete(s.conns, c.id)
	s.subsMu.Unlock()
	if workspaceChanged {
		s.broadcastWorkspaceChanged(workspace, nil)
	}
	s.recountKeepalive()
	s.touch()
	s.logger.Debug("client disconnected", "conn", c.id, "reason", reason)
	runDisconnectHooks(c.id)
	s.loop.Wake()
}

// closeAllConns disconnects every client.
func (s *Server) closeAllConns(reason string) {
	s.subsMu.RLock()
	conns := make([]*Conn, 0, len(s.conns))
	for _, c := range s.conns {
		conns = append(conns, c)
	}
	s.subsMu.RUnlock()
	for _, c := range conns {
		c.closeWithReason(reason)
	}
}

// Shutdown stops the daemon. It broadcasts state.event{daemon_stopping} to
// every subscriber first (spec, daemon.shutdown), gives the writers a moment to
// flush, and then closes the listener.
func (s *Server) Shutdown() {
	s.stopOnce.Do(func() {
		s.graceful.Store(true)
		s.broadcastEvent(state.Event{
			Kind: "daemon_stopping",
			At:   time.Now().Format(time.RFC3339),
		})
		s.drainQueues(500 * time.Millisecond)
		close(s.stopping)
		if s.ln != nil {
			_ = s.ln.Close()
		}
	})
}

// Done is closed once Serve has returned.
func (s *Server) Done() <-chan struct{} { return s.done }

// drainQueues waits, up to timeout, for every connection's outbound queue to
// empty, so a client sees daemon_stopping (and the reply to daemon.shutdown)
// before the socket goes away.
func (s *Server) drainQueues(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.subsMu.RLock()
		pending := 0
		for _, c := range s.conns {
			pending += len(c.out)
		}
		s.subsMu.RUnlock()
		if pending == 0 {
			// One more tick so the writer flushes what it has already taken.
			time.Sleep(20 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ----------------------------------------------------------- subscriptions ---

// Subscribers counts the connections currently subscribed to state.
func (s *Server) Subscribers() int {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	n := 0
	for _, c := range s.conns {
		if c.subscribed {
			n++
		}
	}
	return n
}

// Clients counts open connections, subscribed or not.
func (s *Server) Clients() int {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	return len(s.conns)
}

// demand is the scanner's Demand callback: how many subscribers there are and
// the union of what they asked to have collected.
func (s *Server) demand() (int, scanner.Include) {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	var inc scanner.Include
	n := 0
	for _, c := range s.conns {
		if !c.subscribed {
			continue
		}
		n++
		inc.Stats = inc.Stats || c.include.Stats
		inc.Health = inc.Health || c.include.Health
	}
	return n, inc
}

// subscribe registers a connection as a subscriber and hands it either the
// cached snapshot or the replay window's cursor snapshot as the reply to its
// own state.subscribe call. Both registration and replay queuing happen under
// the publish lock, so no delta can slip in between the opening frame and the
// live stream.
//
// The reply never waits for a scan: the snapshot is whatever the loop last
// published — with stats and health null when the loop has not been collecting
// them — and Wake makes the loop scan immediately, so the fields the subscriber
// asked for arrive in the first delta rather than delaying the reply by a scan.
// A scoped view keeps the daemon's global sequence; gaps are expected when an
// intervening publish changed only rows outside the selected scope.
func (s *Server) subscribe(c *Conn, id json.RawMessage, include scanner.Include, events bool, scope state.Scope, afterSeq uint64) error {
	s.subsMu.Lock()
	if err := s.workspaceScopeErrorLocked(scope); err != nil {
		s.subsMu.Unlock()
		return err
	}
	oldWorkspace, oldChanged := s.detachWorkspaceLocked(c)
	newChanged, err := s.attachWorkspaceLocked(c, scope)
	if err != nil {
		// A failed resubscribe must leave the existing relation intact. This
		// matters when the connection was the sole explicit member of a
		// workspace and tried to switch to a workspace-only scope.
		s.restoreWorkspaceLocked(c, c.scope)
		s.subsMu.Unlock()
		return err
	}
	snap := s.loop.Cached()
	// Workspace membership is an ephemeral relation outside the state
	// sequence. A cursor cannot reconstruct a member that joined or left while
	// this connection was away, so workspace subscriptions always take the
	// current scoped snapshot and begin a fresh stream.
	if strings.TrimSpace(scope.Workspace) != "" {
		afterSeq = 0
	}
	base, replay, resumed := s.replayFrom(afterSeq, snap)
	c.subscribed, c.include, c.events, c.scope = true, include, events, scope
	effective := s.effectiveScopeLocked(scope)
	var result any
	if effective.Empty() {
		result = filterSnapshot(base, include, effective)
	} else {
		base.Ports = filterPorts(base.Ports, include)
		result = state.StreamSnapshotFor(base, effective)
	}
	raw, err := json.Marshal(result)
	if err == nil {
		msg, mErr := json.Marshal(rpc.Response{JSONRPC: rpc.Version, ID: id, Result: raw})
		if mErr == nil {
			c.enqueue(msg)
		}
	}
	if resumed {
		for _, item := range replay {
			s.queueReplay(c, item, effective)
		}
	}
	s.subsMu.Unlock()
	newWorkspace := strings.TrimSpace(scope.Workspace)
	if oldChanged && newChanged && oldWorkspace == newWorkspace {
		s.broadcastWorkspaceChanged(newWorkspace, c)
	} else {
		if oldChanged {
			s.broadcastWorkspaceChanged(oldWorkspace, c)
		}
		if newChanged {
			s.broadcastWorkspaceChanged(newWorkspace, c)
		}
	}

	s.touch()
	s.loop.Wake()
	if err != nil {
		c.replyError(id, rpc.NewError(rpc.CodeInternal, "marshalling snapshot: "+err.Error(), ""))
	}
	return nil
}

// recordReplay keeps the exact transition used for a live broadcast. It is
// called while subsMu is held, so a reconnect cannot replay a transition and
// then receive the same one again from the live publisher.
func (s *Server) recordReplay(prev, next state.Snapshot, events []state.Event) {
	if next.Seq == 0 || next.Seq <= prev.Seq {
		return
	}
	item := stateReplay{prev: prev, next: next, events: append([]state.Event(nil), events...)}
	s.replayMu.Lock()
	if len(s.replay) < stateReplayCapacity {
		s.replay = append(s.replay, item)
	} else {
		// Replace the evicted record, releasing all its snapshot/event refs.
		// Do not copy the entire resume window on every steady-state update.
		s.replay[s.replayStart] = item
		s.replayStart = (s.replayStart + 1) % stateReplayCapacity
	}
	s.replayMu.Unlock()
}

// replayFrom returns a contiguous transition chain whose opening snapshot is
// exactly afterSeq. If the cursor is outside the bounded window, callers get
// the current snapshot and resumed=false, which is the safe full-resync path.
func (s *Server) replayFrom(afterSeq uint64, current state.Snapshot) (state.Snapshot, []stateReplay, bool) {
	if afterSeq == 0 {
		return current, nil, false
	}
	if afterSeq == current.Seq {
		return current, nil, true
	}
	s.replayMu.Lock()
	history := make([]stateReplay, len(s.replay))
	n := copy(history, s.replay[s.replayStart:])
	copy(history[n:], s.replay[:s.replayStart])
	s.replayMu.Unlock()
	start := -1
	var base state.Snapshot
	for i, item := range history {
		switch {
		case item.prev.Seq == afterSeq:
			start, base = i, item.prev
		case item.next.Seq == afterSeq:
			start, base = i+1, item.next
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return current, nil, false
	}
	expected := afterSeq
	for i := start; i < len(history); i++ {
		if history[i].prev.Seq != expected {
			return current, nil, false
		}
		expected = history[i].next.Seq
		if expected == current.Seq {
			return base, history[start : i+1], true
		}
	}
	return current, nil, false
}

func (s *Server) queueReplay(c *Conn, item stateReplay, scope state.Scope) {
	if scope.Empty() {
		if msg := marshalDelta(item.prev, item.next, c.include, scope); msg != nil {
			c.enqueue(msg)
		}
		if c.events && len(item.events) > 0 {
			for _, msg := range marshalEvents(item.events, c.include, scope, item.next.Groups) {
				c.enqueue(msg)
			}
		}
		return
	}
	if !c.events {
		return
	}
	groups := append(append([]state.Group{}, item.prev.Groups...), item.next.Groups...)
	for _, msg := range marshalStateChanges(state.StateChangesForTransition(item.prev, item.next, item.events), scope, groups) {
		c.enqueue(msg)
	}
}

// unsubscribe drops a connection's subscription.
func (s *Server) unsubscribe(c *Conn) {
	s.subsMu.Lock()
	workspace, changed := s.detachWorkspaceLocked(c)
	c.subscribed, c.include, c.events, c.scope = false, scanner.Include{}, false, state.Scope{}
	s.subsMu.Unlock()
	if changed {
		s.broadcastWorkspaceChanged(workspace, nil)
	}
	s.touch()
	s.loop.Wake()
}

func (s *Server) attachWorkspaceLocked(c *Conn, scope state.Scope) (bool, error) {
	workspace := strings.TrimSpace(scope.Workspace)
	if workspace == "" {
		return false, nil
	}
	if err := s.workspaceScopeErrorLocked(scope); err != nil {
		return false, err
	}
	bindings := s.workspaceScopes[workspace]
	changed := false
	if scope.HasSelectors() {
		if bindings == nil {
			bindings = map[uint64]state.Scope{}
			s.workspaceScopes[workspace] = bindings
		}
		concrete := scope.Concrete()
		previous, existed := bindings[c.id]
		changed = !existed || previous.Key() != concrete.Key()
		bindings[c.id] = concrete
	}
	return changed, nil
}

func (s *Server) workspaceScopeErrorLocked(scope state.Scope) error {
	workspace := strings.TrimSpace(scope.Workspace)
	if workspace == "" || scope.HasSelectors() || len(s.workspaceScopes[workspace]) > 0 {
		return nil
	}
	return rpc.NewError(rpc.CodeInvalidParams,
		"workspace has no registered worktrees or repositories",
		"subscribe once with --workspace and an explicit --worktree or --repository")
}

func (s *Server) detachWorkspaceLocked(c *Conn) (string, bool) {
	workspace := strings.TrimSpace(c.scope.Workspace)
	if workspace == "" {
		return "", false
	}
	bindings := s.workspaceScopes[workspace]
	if bindings == nil {
		return workspace, false
	}
	_, existed := bindings[c.id]
	delete(bindings, c.id)
	if len(bindings) == 0 {
		delete(s.workspaceScopes, workspace)
	}
	return workspace, existed
}

func (s *Server) restoreWorkspaceLocked(c *Conn, scope state.Scope) {
	workspace := strings.TrimSpace(scope.Workspace)
	if workspace == "" || !scope.HasSelectors() {
		return
	}
	bindings := s.workspaceScopes[workspace]
	if bindings == nil {
		bindings = map[uint64]state.Scope{}
		s.workspaceScopes[workspace] = bindings
	}
	bindings[c.id] = scope.Concrete()
}

func (s *Server) effectiveScopeLocked(scope state.Scope) state.Scope {
	workspace := strings.TrimSpace(scope.Workspace)
	if workspace == "" {
		return scope
	}
	out := scope
	for _, binding := range s.workspaceScopes[workspace] {
		out = out.Merge(binding)
	}
	out.Workspace = workspace
	return out
}

func (s *Server) effectiveScope(scope state.Scope) state.Scope {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	return s.effectiveScopeLocked(scope)
}

func (s *Server) workspaceAvailable(workspace string) bool {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return false
	}
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	return len(s.workspaceScopes[workspace]) > 0
}

// broadcastWorkspaceChanged asks live members of a workspace to refresh the
// relation-derived snapshot. A new member receives its own opening snapshot;
// existing members need this explicit fact when a sibling joins or leaves so
// they never infer a new relation from an increment alone.
func (s *Server) broadcastWorkspaceChanged(workspace string, exclude *Conn) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return
	}
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	snap := s.loop.Cached()
	change := state.StateChanged{
		Type:          "state.changed",
		EventID:       state.OpaqueEventID(state.NewEventID(s.nextWorkspaceEvent.Add(1), "workspace_changed", state.OpaqueEventID(workspace))),
		Seq:           snap.Seq,
		StateRevision: state.StateRevisionForSeq(snap.Seq),
		Changed:       []string{"workspace_changed"},
		ObservedAt:    snap.At,
	}
	raw, err := json.Marshal(rpc.Notification{
		JSONRPC: rpc.Version,
		Method:  rpc.MethodStateChanged,
		Params:  mustJSON(change),
	})
	if err != nil {
		return
	}
	for _, c := range s.conns {
		if c == exclude || !c.subscribed || !c.events || strings.TrimSpace(c.scope.Workspace) != workspace {
			continue
		}
		c.enqueue(raw)
	}
}

// publish fans a scanner transition out to subscribers. Scoped connections
// receive only their redacted state.changed summaries; legacy connections keep
// the historical delta/event view. Each wire view is marshalled once.
func (s *Server) publish(prev, next state.Snapshot, events []state.Event) {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	s.recordReplay(prev, next, events)

	// Events raised before anyone was listening ride out with the first delta
	// that has somewhere to go.
	if s.listeningForEvents() {
		events = append(s.takePending(), events...)
	}

	deltaCache := map[viewKey][]byte{}
	eventCache := map[viewKey][][]byte{}
	changeCache := map[viewKey][][]byte{}

	for _, c := range s.conns {
		if !c.subscribed {
			continue
		}
		effective := s.effectiveScopeLocked(c.scope)
		view := viewKey{include: c.include, scope: effective.Key()}
		if !effective.Empty() {
			if !c.events {
				continue
			}
			msgs, ok := changeCache[view]
			if !ok {
				changes := state.StateChangesForTransition(prev, next, events)
				groups := append(append([]state.Group{}, prev.Groups...), next.Groups...)
				msgs = marshalStateChanges(changes, effective, groups)
				changeCache[view] = msgs
			}
			for _, m := range msgs {
				c.enqueue(m)
			}
			continue
		}
		msg, ok := deltaCache[view]
		if !ok {
			msg = marshalDelta(prev, next, c.include, c.scope)
			deltaCache[view] = msg
		}
		if msg != nil {
			c.enqueue(msg)
		}
		if !c.events || len(events) == 0 {
			continue
		}
		msgs, ok := eventCache[view]
		if !ok {
			msgs = marshalEvents(events, c.include, c.scope, next.Groups)
			eventCache[view] = msgs
		}
		for _, m := range msgs {
			c.enqueue(m)
		}
	}
}

// viewKey identifies one subscriber's include set. The delta is marshalled
// once per distinct view rather than once per subscriber.
type viewKey struct {
	include scanner.Include
	scope   string
}

// listeningForEvents reports whether any subscriber asked for events. Caller
// holds subsMu.
func (s *Server) listeningForEvents() bool {
	for _, c := range s.conns {
		if c.subscribed && c.events {
			return true
		}
	}
	return false
}

// BroadcastEvent sends one event to subscribers that asked for events, applying
// the scoped relation before it reaches the wire.
func (s *Server) BroadcastEvent(ev state.Event) { s.broadcastEvent(ev) }

// broadcastEvent sends one event to matching subscribers that asked for events.
func (s *Server) broadcastEvent(ev state.Event) {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	cache := map[viewKey][][]byte{}
	groups := s.loop.Cached().Groups
	for _, c := range s.conns {
		if !c.subscribed || !c.events {
			continue
		}
		effective := s.effectiveScopeLocked(c.scope)
		view := viewKey{include: c.include, scope: effective.Key()}
		if !effective.Empty() {
			change := state.StateChangedForEvent(ev, s.loop.Cached().Seq, groups)
			if !effective.MatchChangedIn(change, groups) {
				continue
			}
			raw, err := json.Marshal(rpc.Notification{JSONRPC: rpc.Version, Method: rpc.MethodStateChanged, Params: mustJSON(change)})
			if err == nil {
				c.enqueue(raw)
			}
			continue
		}
		msgs, ok := cache[view]
		if !ok {
			msgs = marshalEvents([]state.Event{ev}, c.include, c.scope, groups)
			cache[view] = msgs
		}
		for _, m := range msgs {
			c.enqueue(m)
		}
	}
}

// marshalDelta builds one state.delta notification for a given include set.
// It returns nil when the delta is empty for this subscriber, so a client that
// asked for neither stats nor health is not woken by a stats-only tick.
func marshalDelta(prev, next state.Snapshot, include scanner.Include, scope state.Scope) []byte {
	prev = scope.FilterSnapshot(prev)
	next = scope.FilterSnapshot(next)
	var d state.Delta
	if include.Stats {
		d = state.DiffWithStats(prev, next)
	} else {
		d = state.Diff(prev, next)
	}
	if emptyDelta(d) {
		return nil
	}
	d.Ports.Added = filterPorts(d.Ports.Added, include)
	d.Ports.Updated = filterPorts(d.Ports.Updated, include)

	raw, err := json.Marshal(d)
	if err != nil {
		return nil
	}
	msg, err := json.Marshal(rpc.Notification{
		JSONRPC: rpc.Version, Method: rpc.MethodStateDelta, Params: raw,
	})
	if err != nil {
		return nil
	}
	return msg
}

// marshalEvents builds one state.event notification per event.
func marshalEvents(events []state.Event, include scanner.Include, scope state.Scope, groups []state.Group) [][]byte {
	out := make([][]byte, 0, len(events))
	for _, ev := range events {
		if !scope.MatchEvent(ev, groups) {
			continue
		}
		if ev.Port != nil {
			p := filterPort(*ev.Port, include)
			ev.Port = &p
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		msg, err := json.Marshal(rpc.Notification{JSONRPC: rpc.Version, Method: rpc.MethodStateEvent, Params: raw})
		if err != nil {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func marshalStateChanges(changes []state.StateChanged, scope state.Scope, groups []state.Group) [][]byte {
	out := make([][]byte, 0, len(changes))
	for _, change := range changes {
		if !scope.MatchChangedIn(change, groups) {
			continue
		}
		raw, err := json.Marshal(rpc.Notification{
			JSONRPC: rpc.Version,
			Method:  rpc.MethodStateChanged,
			Params:  mustJSON(change),
		})
		if err != nil {
			continue
		}
		out = append(out, raw)
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func emptyDelta(d state.Delta) bool {
	return len(d.Ports.Added) == 0 && len(d.Ports.Updated) == 0 && len(d.Ports.Removed) == 0 &&
		len(d.Groups.Added) == 0 && len(d.Groups.Updated) == 0 && len(d.Groups.Removed) == 0 &&
		len(d.Sessions.Added) == 0 && len(d.Sessions.Updated) == 0 && len(d.Sessions.Removed) == 0
}

// filterSnapshot strips enrichments this subscriber did not ask for.
func filterSnapshot(snap state.Snapshot, include scanner.Include, scope state.Scope) state.Snapshot {
	snap.Ports = filterPorts(snap.Ports, include)
	snap = scope.FilterSnapshot(snap)
	if snap.Groups == nil {
		snap.Groups = []state.Group{}
	}
	if snap.Sessions == nil {
		snap.Sessions = []state.SessionRecord{}
	}
	return snap
}

func filterPorts(pp []state.Port, include scanner.Include) []state.Port {
	if include.Stats && include.Health {
		return pp
	}
	out := make([]state.Port, len(pp))
	for i := range pp {
		out[i] = filterPort(pp[i], include)
	}
	return out
}

// filterPort strips what this subscriber did not ask for. Health a
// `oberth.yaml` asked the daemon to poll is the exception: the config declared
// it, so it is state every subscriber sees whether or not it asked for
// `include: ["health"]` (step 1A.7).
func filterPort(p state.Port, include scanner.Include) state.Port {
	if !include.Stats {
		p.Stats = nil
	}
	if !include.Health && p.Health != nil && !p.Health.Configured {
		p.Health = nil
	}
	return p
}

// ------------------------------------------------------------------- idle ---

// touch records client activity, resetting the idle countdown.
func (s *Server) touch() { s.lastActive.Store(time.Now().UnixNano()) }

// recountKeepalive recomputes how many connected clients asked the daemon to
// stay up (daemon.hello{keepalive:true}).
func (s *Server) recountKeepalive() {
	s.subsMu.RLock()
	n := int64(0)
	for _, c := range s.conns {
		if c.Keepalive() {
			n++
		}
	}
	s.subsMu.RUnlock()
	s.keepalives.Store(n)
}

// watchIdle stops the daemon once it has been idle for IdleTimeout. A
// keepalive client or any subscriber counts as activity, so the desktop app
// keeps the daemon alive simply by staying connected.
func (s *Server) watchIdle(ctx context.Context) {
	if s.opts.IdleTimeout <= 0 {
		return
	}
	tick := idleCheckInterval
	if s.opts.IdleTimeout/4 < tick {
		tick = s.opts.IdleTimeout / 4
	}
	if tick <= 0 {
		tick = time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopping:
			return
		case <-t.C:
			if s.keepalives.Load() > 0 || s.Subscribers() > 0 {
				s.touch()
				continue
			}
			idle := time.Since(time.Unix(0, s.lastActive.Load()))
			if idle >= s.opts.IdleTimeout {
				s.logger.Info("idle timeout reached, stopping",
					"idle", idle.Round(time.Second).String())
				s.Shutdown()
				return
			}
		}
	}
}
