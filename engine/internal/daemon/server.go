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

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// DefaultIdleTimeout is the config default for daemon.idle_timeout; zero means
// the daemon never idles out.
const DefaultIdleTimeout = 30 * time.Minute

// idleCheckInterval caps how often the idle watchdog can wake.
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

// Options configures a Server. Every field may be left zero; New fills in the
// empty ones.
type Options struct {
	// Identity and location.
	Socket     string // listen address; empty means SocketPath()
	Version    string // the daemon's own version, reported by daemon.hello
	BinaryPath string // what a client re-execs to autostart us
	DBPath     string // SQLite file; empty means store.Path()

	// Lifetime.
	IdleTimeout time.Duration // stop after this long with nobody watching; zero disables

	// Cadence. Both are ignored when Scanner is set: the caller then built the
	// loop and chose its own intervals.
	StatsInterval time.Duration // `daemon.stats_interval`; zero means the scanner default
	ScanInterval  time.Duration // `daemon.scan_interval`; zero means the scanner default

	// Collaborators. Logger is required in production and may be nil in tests,
	// which get a discard logger.
	Logger  *slog.Logger
	Lock    *Lock         // an already-acquired lock; nil means Serve takes one and releases it
	Scanner *scanner.Loop // an injected loop; nil means the OS scanner
}

// Server is the daemon: the listener it owns, the connections it tracks, the
// fan-out that feeds subscribers, and its own lifecycle.
type Server struct {
	opts    Options
	logger  *slog.Logger
	socket  string
	runtime *Runtime
	loop    *scanner.Loop
	ln      net.Listener

	ownLock bool
	lock    *Lock

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

	graceful atomic.Bool
	wg       sync.WaitGroup
	stopOnce sync.Once
	stopping chan struct{}
	done     chan struct{}
	// producers counts stream goroutines across every connection. They run
	// after their handler returns and outlive the connection entry, which
	// removeConn deletes, so they are counted on the server rather than the
	// Conn: shutdown must wait for them before it closes the store.
	producers sync.WaitGroup
}

type stateReplay struct {
	prev   state.Snapshot
	next   state.Snapshot
	events []state.Event
}

// New builds a Server. It does not listen; call Serve.
func New(opts Options) *Server {
	opts = withServerDefaults(opts)
	server := &Server{
		opts:            opts,
		logger:          opts.Logger,
		socket:          opts.Socket,
		conns:           map[uint64]*Conn{},
		workspaceScopes: map[string]map[uint64]state.Scope{},
		stopping:        make(chan struct{}),
		done:            make(chan struct{}),
	}
	server.touch()

	server.loop = server.scannerFor(opts)
	server.loop.SetDemand(server.demand)
	server.loop.SetPublisher(server.publish)
	server.runtime = server.buildRuntime(opts)
	server.loop.SetRuns(server.runtime.RunRegistry)
	return server
}

// withServerDefaults fills in whatever a caller left empty, so no later step of
// construction has to ask.
func withServerDefaults(opts Options) Options {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Socket == "" {
		opts.Socket = SocketPath()
	}
	return opts
}

// scannerFor returns the scanner the server should drive: the injected one when
// a test supplied it, otherwise a new loop configured from these options.
func (s *Server) scannerFor(opts Options) *scanner.Loop {
	if opts.Scanner != nil {
		return opts.Scanner
	}
	return scanner.New(scanner.Options{
		DaemonVersion: opts.Version,
		Logger:        opts.Logger,
		StatsInterval: opts.StatsInterval,
		ScanInterval:  opts.ScanInterval,
	})
}

// buildRuntime records what this process is: its version, socket, binary and
// start time, plus the handle the run registry hangs off.
func (s *Server) buildRuntime(opts Options) *Runtime {
	binary := opts.BinaryPath
	if binary == "" {
		// Best effort: a process that cannot name its own binary still serves,
		// it just cannot tell a client where the CLI lives.
		binary, _ = os.Executable()
	}
	return &Runtime{
		StartedAt:  time.Now(),
		Version:    opts.Version,
		Socket:     s.socket,
		BinaryPath: binary,
		PID:        os.Getpid(),
		Logger:     opts.Logger,
		Scanner:    s.loop,
		srv:        s,
	}
}

// Runtime is the runtime handed to handlers and extension hooks.
func (s *Server) Runtime() *Runtime { return s.runtime }

// Socket is the address the server listens on.
func (s *Server) Socket() string { return s.socket }

// acquireSingleInstance takes the lock that makes this process the only daemon
// on this socket. A lock the caller injected is borrowed, so Serve does not own
// it and must not release it.
func (s *Server) acquireSingleInstance() error {
	if s.opts.Lock != nil {
		s.lock = s.opts.Lock
		return nil
	}
	lock, err := AcquireLock(lockPathFor(s.socket))
	if err != nil {
		return err
	}
	s.lock, s.ownLock = lock, true
	return nil
}

// releaseSingleInstance gives up the lock, but only when this server took it.
func (s *Server) releaseSingleInstance() {
	if s.ownLock {
		_ = s.lock.Release()
	}
}

// Serve takes the single-instance lock, listens, then accepts connections until
// ctx is cancelled or Shutdown is called. A second daemon on the same socket
// gets ErrAlreadyRunning.
func (s *Server) Serve(ctx context.Context) error {
	defer close(s.done)

	if err := s.acquireSingleInstance(); err != nil {
		return err
	}
	defer s.releaseSingleInstance()

	// The lock is ours, so a socket file still on disk was left behind by a
	// daemon that died without cleaning up.
	err := removeStaleSocket(s.socket)
	if err != nil {
		return fmt.Errorf("removing stale socket %s: %w", s.socket, err)
	}

	s.ln, err = listen(s.socket)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.socket, err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Opening the store before the scanner lets the very first tick resolve
	// groups with the stored pins and publish the stored names.
	s.openStore()
	defer s.closeStore()

	// Restore required ownership before starting the scanner or accepting RPC.
	// Optional hooks retain their existing adapter, but failed required work
	// must not be presented as an empty successful daemon.
	if err := runStartHooks(s.runtime); err != nil {
		_ = s.ln.Close()
		runShutdownHooks(false)
		_ = removeStaleSocket(s.socket)
		return fmt.Errorf("starting daemon: %w", err)
	}

	s.runBackground(func() { s.loop.Run(ctx) })
	s.runBackground(func() { s.watchIdle(ctx) })

	s.logger.Info("daemon listening",
		"socket", s.socket, "pid", s.runtime.PID, "version", s.opts.Version,
		"idle_timeout", s.opts.IdleTimeout.String())

	go s.shutdownWhenDone(ctx)

	acceptErr := s.accept(ctx)

	cancel()
	s.closeAllConns("daemon stopping")
	s.wg.Wait()
	// A stream producer outlives the handler that started it and writes run and
	// exit facts. closeAllConns only asks it to stop; it has to be waited for
	// before the store closes underneath it.
	s.waitProducers()
	runShutdownHooks(s.graceful.Load())
	_ = removeStaleSocket(s.socket)
	s.logger.Info("daemon stopped", "graceful", s.graceful.Load())
	return acceptErr
}

// waitProducers blocks until every stream producer the server has started has
// returned. Connections are already closed by the time this runs, so no new
// producer can start.
func (s *Server) waitProducers() { s.producers.Wait() }

// runBackground starts one goroutine and counts it in s.wg, so Serve returns
// only after everything it started has stopped rather than merely been told to.
func (s *Server) runBackground(work func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		work()
	}()
}

// shutdownWhenDone stops the daemon when the serve context ends. A shutdown
// that is already under way needs nothing from here.
func (s *Server) shutdownWhenDone(ctx context.Context) {
	select {
	case <-ctx.Done():
		s.Shutdown()
	case <-s.stopping:
	}
}

// accept is the accept loop.
//
// A failure here is only a real failure when the listener was not supposed to
// be closing. Shutdown closing it, or any net.ErrClosed from elsewhere, is the
// normal way this loop ends.
func (s *Server) accept(ctx context.Context) error {
	for {
		conn, err := s.ln.Accept()
		if err == nil {
			s.startConn(ctx, conn)
			continue
		}
		if s.closing() || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return fmt.Errorf("accept: %w", err)
	}
}

// closing reports whether Shutdown has begun, which is what makes the listener
// closing an expected event instead of a failure.
func (s *Server) closing() bool {
	select {
	case <-s.stopping:
		return true
	default:
		return false
	}
}

// startConn registers a connection and starts its read and write loops.
func (s *Server) startConn(ctx context.Context, nc net.Conn) {
	c := newConn(s.nextConnID.Add(1), s, nc)
	s.registerConn(c)
	s.touch()
	s.logger.Debug("client connected", "conn", c.id)

	// The writer is tracked on the connection, not on the server: it must be
	// known to be finished before the socket is closed, and that ordering is
	// the connection's own business.
	c.writerWG.Add(1)
	go func() { c.writeLoop() }()

	s.runBackground(func() {
		// Closing here means a client that stops sending tears down its own
		// connection, whether it disconnected or hit an error.
		defer c.Close()
		c.readLoop(ctx)
	})
}

// registerConn adds a connection to the registry under the lock the publisher
// also takes, so no broadcast can miss a connection that is already answering.
func (s *Server) registerConn(c *Conn) {
	s.subsMu.Lock()
	s.conns[c.id] = c
	s.subsMu.Unlock()
}

// removeConn drops a connection from the registry. Called once per connection,
// from Conn.closeWithReason.
func (s *Server) removeConn(c *Conn, reason string) {
	workspace, workspaceChanged := s.forgetConn(c)
	if workspaceChanged {
		s.broadcastWorkspaceChanged(workspace, nil)
	}

	// The keepalive count is recomputed before anything can ask whether an idle
	// daemon may stop, and the wake makes the next scan see the ports this
	// client was watching go quiet.
	s.markDisconnected()
	s.logger.Debug("client disconnected", "conn", c.id, "reason", reason)
	runDisconnectHooks(c.id)
	s.loop.Wake()
}

// markDisconnected records that a client went away: the keepalive count drops
// first, then the idle clock restarts.
func (s *Server) markDisconnected() {
	s.recountKeepalive()
	s.touch()
}

// forgetConn drops a connection from the registry, detaching any workspace
// relation it held on the way out. The workspace is returned rather than
// broadcast here, because the broadcast must not run under subsMu.
func (s *Server) forgetConn(c *Conn) (string, bool) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	workspace, _, held := s.detachWorkspaceLocked(c)
	delete(s.conns, c.id)
	return workspace, held
}

// connsSnapshot returns the connections registered right now, so a caller can
// act on them — closing, draining — without holding the registry lock across
// the I/O.
func (s *Server) connsSnapshot() []*Conn {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	conns := make([]*Conn, 0, len(s.conns))
	for _, conn := range s.conns {
		conns = append(conns, conn)
	}
	return conns
}

// closeAllConns disconnects every client.
func (s *Server) closeAllConns(reason string) {
	for _, conn := range s.connsSnapshot() {
		conn.closeWithReason(reason)
	}
}

// Shutdown stops the daemon. Subscribers first get
// state.event{daemon_stopping} (spec, daemon.shutdown), the writers then get a
// moment to flush, and only after that does the listener close.
func (s *Server) Shutdown() {
	s.stopOnce.Do(func() {
		s.graceful.Store(true)
		s.announceStopping()
		s.drainQueues(500 * time.Millisecond)
		close(s.stopping)
		if s.ln != nil {
			_ = s.ln.Close()
		}
	})
}

// announceStopping tells every subscriber the daemon is going away, so a client
// learns it before the socket does rather than seeing an unexplained close.
func (s *Server) announceStopping() {
	s.broadcastEvent(state.Event{
		Kind: "daemon_stopping",
		At:   time.Now().Format(time.RFC3339),
	})
}

// Done is closed once Serve has returned.
func (s *Server) Done() <-chan struct{} { return s.done }

// drainQueues gives every connection's outbound queue up to timeout to empty,
// so daemon_stopping (and the reply to daemon.shutdown) reaches a client before
// the socket goes away.
func (s *Server) drainQueues(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.pendingWrites() == 0 {
			// One more tick so a writer can flush what it has already taken
			// off its queue.
			time.Sleep(20 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pendingWrites counts messages queued but not yet written, across every
// connection.
func (s *Server) pendingWrites() int {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	pending := 0
	for _, conn := range s.conns {
		pending += len(conn.out)
	}
	return pending
}

// ----------------------------------------------------------- subscriptions ---

// Subscribers counts the connections currently subscribed to state. It is
// demand's count without the include set, so the two cannot disagree about who
// is watching.
func (s *Server) Subscribers() int {
	subscribers, _ := s.demand()
	return subscribers
}

// Clients is the number of open connections, whether subscribed or not.
func (s *Server) Clients() int {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	return len(s.conns)
}

// demand is the scanner's Demand callback: the subscriber count and the union
// of what those subscribers asked to have collected.
func (s *Server) demand() (int, scanner.Include) {
	s.subsMu.RLock()
	// Read under the same lock the publisher takes, so the count and the
	// include set describe one and the same registry.
	defer s.subsMu.RUnlock()

	var wanted scanner.Include
	subscribers := 0
	for _, c := range s.conns {
		if !c.subscribed {
			continue
		}
		subscribers++
		// The include set is a union, not a majority: one subscriber asking for
		// stats means the loop must collect them for everybody.
		wanted.Stats = wanted.Stats || c.include.Stats
		wanted.Health = wanted.Health || c.include.Health
	}
	return subscribers, wanted
}

// subscribedReply is the opening frame a subscriber receives: the whole
// snapshot for an unscoped subscription, or the stream projection for a scoped
// one. The two shapes differ, which is why this is not just a filter call.
func subscribedReply(base state.Snapshot, include scanner.Include, scope state.Scope) any {
	if scope.Empty() {
		return filterSnapshot(base, include, scope)
	}
	base.Ports = filterPorts(base.Ports, include)
	return state.StreamSnapshotFor(base, scope)
}

// marshalSnapshot frames the opening reply of a state.subscribe. It is a
// variable because that frame is the one reply the daemon marshals by hand, and
// the failure path is a contract worth a regression: a value that cannot be
// encoded must be answered with an error and must leave nothing registered.
// Nothing outside this package can hand subscribe an unencodable snapshot, so
// the seam is a test hook rather than a shape change on the wire.
var marshalSnapshot = json.Marshal

// subscribe registers a connection as a subscriber and hands it either the
// cached snapshot or the replay window's cursor snapshot as the reply to its
// own state.subscribe call. Both registration and replay queuing happen under
// the publish lock, so no delta can slip in between the opening frame and the
// live stream.
//
// The reply never waits on a scan: it carries whatever the loop last published,
// with stats and health null while the loop is not collecting them. Wake makes
// the loop scan at once, so the fields a subscriber asked for arrive in the
// first delta instead of holding the reply back by a scan. A scoped view keeps
// the daemon's global sequence; gaps are expected when an intervening publish
// changed only rows outside the selected scope.
//
// Nothing is registered until the opening frame has been encoded: the old
// workspace relation is detached, the new one is validated and the reply is
// framed first, and only a frame that encoded is committed to the registry. A
// snapshot that cannot be marshalled is an engine fault, and answering it with
// an error while leaving a subscription behind would have the client waiting
// for a stream it was told it never got, keep the scanner collecting for a
// subscriber that never saw a sequence, and — for a workspace member — leak
// selectors the rest of that workspace would union into its own view. The error
// is written before the lock is dropped so a later delta cannot overtake it.
func (s *Server) subscribe(c *Conn, id json.RawMessage, include scanner.Include, events bool, scope state.Scope, afterSeq uint64) error {
	s.subsMu.Lock()
	// Detached before the new view is validated and computed, for two reasons:
	// a workspace-only resubscribe by the last explicit member must see that it
	// has nobody to inherit from, and the connection's own stale selectors must
	// not leak into the reply it is about to receive.
	oldWorkspace, previous, oldChanged := s.detachWorkspaceLocked(c)
	if err := s.workspaceScopeErrorLocked(scope); err != nil {
		// A failed resubscribe must leave the existing relation intact, so the
		// detach above is undone before the error goes back: by the time the
		// lock is dropped no peer can tell this call ever ran.
		s.restoreWorkspaceLocked(c, oldWorkspace, previous, oldChanged)
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
	// The view the client is about to have: its own selectors plus every
	// current member of its workspace. The union is the same whether it is
	// taken before or after this connection's own binding is written, which is
	// what lets the reply be framed before the commit below.
	effective := s.effectiveScopeLocked(scope)
	raw, mErr := marshalSnapshot(subscribedReply(base, include, effective))
	if mErr != nil {
		// Nothing was registered and nothing was queued, so there is no
		// subscription to roll back — only the relation this call detached,
		// which the undo puts back exactly as it was.
		s.restoreWorkspaceLocked(c, oldWorkspace, previous, oldChanged)
		c.replyError(id, rpc.NewError(rpc.CodeInternal, "marshalling snapshot: "+mErr.Error(), ""))
		s.subsMu.Unlock()
		s.touch()
		return nil
	}
	newChanged := s.bindWorkspaceLocked(c, scope)
	c.subscribed, c.include, c.events, c.scope = true, include, events, scope
	c.sendResponse(id, raw, nil)
	if resumed {
		for _, item := range replay {
			s.queueReplay(c, item, effective)
		}
	}
	s.subsMu.Unlock()
	s.announceWorkspaceChange(oldWorkspace, strings.TrimSpace(scope.Workspace), oldChanged, newChanged, c)

	s.touch()
	s.loop.Wake()
	return nil
}

// announceWorkspaceChange tells the peers of any workspace whose membership
// changed to refresh their relation-derived view. Staying in the same workspace
// with a changed relation is one announcement; moving between workspaces tells
// the one it left and the one it joined. The reconnecting connection is always
// excluded, because it already received its own opening snapshot.
func (s *Server) announceWorkspaceChange(oldWorkspace, newWorkspace string, oldChanged, newChanged bool, c *Conn) {
	if oldChanged && newChanged && oldWorkspace == newWorkspace {
		s.broadcastWorkspaceChanged(newWorkspace, c)
		return
	}
	if oldChanged {
		s.broadcastWorkspaceChanged(oldWorkspace, c)
	}
	if newChanged {
		s.broadcastWorkspaceChanged(newWorkspace, c)
	}
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
		s.queueUnscopedReplay(c, item, scope)
		return
	}
	s.queueScopedReplay(c, item, scope)
}

// queueUnscopedReplay sends a whole transition to a client watching everything:
// one delta, then the events that transition produced, if it wants events.
func (s *Server) queueUnscopedReplay(c *Conn, item stateReplay, scope state.Scope) {
	view := viewSpec{include: c.include, scope: scope, events: c.events}
	if msg := deltaFrame(item.prev, item.next, view); msg != nil {
		c.enqueue(msg)
	}
	if !view.events {
		return
	}
	for _, msg := range eventFrames(item.events, view, item.next.Groups) {
		c.enqueue(msg)
	}
}

// queueScopedReplay sends only what falls inside the client's scope, and only
// to a client that asked for events. The group set spans both sides of the
// transition, because a scope may name a group that the change removes.
func (s *Server) queueScopedReplay(c *Conn, item stateReplay, scope state.Scope) {
	if !c.events {
		return
	}
	groups := append(append([]state.Group{}, item.prev.Groups...), item.next.Groups...)
	changes := state.StateChangesForTransition(item.prev, item.next, item.events)
	for _, msg := range marshalStateChanges(changes, scope, groups) {
		c.enqueue(msg)
	}
}

// unsubscribe drops a connection's subscription.
func (s *Server) unsubscribe(c *Conn) {
	s.subsMu.Lock()
	workspace, _, held := s.detachWorkspaceLocked(c)
	c.subscribed, c.include, c.events, c.scope = false, scanner.Include{}, false, state.Scope{}
	s.subsMu.Unlock()
	if held {
		s.broadcastWorkspaceChanged(workspace, nil)
	}
	s.touch()
	s.loop.Wake()
}

// bindWorkspaceLocked commits a connection's explicit relation to its workspace
// and reports whether the peer-visible membership changed. It is the write half
// of a subscribe: callers validate and frame the reply first, so a subscription
// that never reaches a client never reaches the registry either.
func (s *Server) bindWorkspaceLocked(c *Conn, scope state.Scope) bool {
	workspace := strings.TrimSpace(scope.Workspace)
	if workspace == "" || !scope.HasSelectors() {
		return false
	}
	concrete := scope.Concrete()
	bindings := s.workspaceScopes[workspace]
	previous, existed := bindings[c.id]
	if bindings == nil {
		bindings = map[uint64]state.Scope{}
		s.workspaceScopes[workspace] = bindings
	}
	bindings[c.id] = concrete
	return !existed || previous.Key() != concrete.Key()
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

// detachWorkspaceLocked removes whatever relation the connection holds and
// returns it, so the caller — a resubscribe that may still fail, or a
// disconnect that is final — decides what happens to it. The concrete relation
// is returned rather than re-read from c.scope, because c.scope is still the
// old subscription when a rollback runs.
func (s *Server) detachWorkspaceLocked(c *Conn) (workspace string, previous state.Scope, held bool) {
	workspace = strings.TrimSpace(c.scope.Workspace)
	if workspace == "" {
		return "", state.Scope{}, false
	}
	bindings := s.workspaceScopes[workspace]
	if bindings == nil {
		return workspace, state.Scope{}, false
	}
	previous, held = bindings[c.id]
	delete(bindings, c.id)
	if len(bindings) == 0 {
		delete(s.workspaceScopes, workspace)
	}
	return workspace, previous, held
}

// restoreWorkspaceLocked puts back what detachWorkspaceLocked took away, and
// nothing else. It is the undo of a subscribe that failed before it committed:
// restoring a member that was never there would leave a failed request with a
// visible side effect, and dropping the member it displaced would take a
// worktree out of view for peers that never saw a change.
func (s *Server) restoreWorkspaceLocked(c *Conn, workspace string, previous state.Scope, held bool) {
	if workspace == "" || !held {
		return
	}
	bindings := s.workspaceScopes[workspace]
	if bindings == nil {
		bindings = map[uint64]state.Scope{}
		s.workspaceScopes[workspace] = bindings
	}
	bindings[c.id] = previous
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
	raw, ok := frameNotification(rpc.MethodStateChanged, s.workspaceChange(workspace))
	if !ok {
		return
	}
	for _, c := range s.conns {
		if c.receivesWorkspaceChange(workspace, exclude) {
			c.enqueue(raw)
		}
	}
}

// workspaceChange is the redacted summary a membership change publishes: it
// carries no rows, only the fact that members should refresh the
// relation-derived snapshot.
func (s *Server) workspaceChange(workspace string) state.StateChanged {
	snap := s.loop.Cached()
	return state.StateChanged{
		Type:          "state.changed",
		EventID:       state.OpaqueEventID(state.NewEventID(s.nextWorkspaceEvent.Add(1), "workspace_changed", state.OpaqueEventID(workspace))),
		Seq:           snap.Seq,
		StateRevision: state.StateRevisionForSeq(snap.Seq),
		Changed:       []string{"workspace_changed"},
		ObservedAt:    snap.At,
	}
}

// receivesWorkspaceChange reports whether one connection is a peer of the
// workspace that changed and should refresh: the connection that caused the
// change, non-subscribers, event-less viewers and members of another workspace
// are all excluded.
func (c *Conn) receivesWorkspaceChange(workspace string, exclude *Conn) bool {
	if c == exclude || !c.subscribed || !c.events {
		return false
	}
	return strings.TrimSpace(c.scope.Workspace) == workspace
}

// changeSet is one transition as every scoped subscriber of a view sees it:
// the redacted summaries built once, plus the group rows they are matched
// against. Building it is per transition, not per subscriber.
type changeSet struct {
	changes []state.StateChanged
	groups  []state.Group
}

func newChangeSet(prev, next state.Snapshot, events []state.Event) changeSet {
	return changeSet{
		changes: state.StateChangesForTransition(prev, next, events),
		groups:  append(append([]state.Group{}, prev.Groups...), next.Groups...),
	}
}

// eventChangeSet is the same view of a single standalone event: the redacted
// summary the scoped fan-out routes, matched against the rows that event
// happened on.
func eventChangeSet(ev state.Event, snap state.Snapshot) changeSet {
	return changeSet{
		changes: []state.StateChanged{state.StateChangedForEvent(ev, snap.Seq, snap.Groups)},
		groups:  snap.Groups,
	}
}

// viewSpec is what one connection is entitled to receive. It is derived from
// the connection once, under subsMu, and is the only input the frame
// constructors read: the fields the subscriber asked to collect (include), the
// rows its scope selects, and whether it wants the event stream at all.
type viewSpec struct {
	include scanner.Include
	scope   state.Scope
	events  bool
}

// scoped reports whether this view is narrower than the daemon's machine-wide
// view. A scoped view is served redacted summaries and never the raw delta.
func (v viewSpec) scoped() bool { return !v.scope.Empty() }

// viewKey is the cache identity of a view. Every dimension a frame depends on
// belongs in it: if two viewers that receive different frames shared a key, the
// first one encoded would define the other's stream. events is in the key for
// exactly that reason.
type viewKey struct {
	include scanner.Include
	scope   string
	events  bool
}

func (v viewSpec) key() viewKey {
	return viewKey{include: v.include, scope: v.scope.Key(), events: v.events}
}

// frames is one view's wire output: the delta and/or events of an unscoped
// viewer, or the redacted summaries of a scoped one. The kinds are never mixed
// for a single view.
type frames struct {
	delta   []byte
	events  [][]byte
	changes [][]byte
}

// frameCache encodes a fan-out once per distinct view. The key is built by
// viewSpec.key, not by hand at each call site, so a dimension added to the view
// cannot be forgotten by one publisher and remembered by the other.
type frameCache struct {
	frames map[viewKey]frames
}

func newFrameCache() *frameCache {
	return &frameCache{frames: map[viewKey]frames{}}
}

// forView returns the frames already encoded for this view, or builds and keeps
// them. The build closure must depend on nothing the key omits.
func (fc *frameCache) forView(view viewSpec, build func() frames) frames {
	if encoded, known := fc.frames[view.key()]; known {
		return encoded
	}
	encoded := build()
	fc.frames[view.key()] = encoded
	return encoded
}

// transitionFrames encodes a published scan transition for one view. An
// unscoped viewer gets the delta and, when it subscribed to events, the events
// this transition raised; a scoped viewer gets only the redacted summaries of
// the rows its scope selects, and only if it asked for events.
func transitionFrames(prev, next state.Snapshot, events []state.Event, set changeSet, view viewSpec) frames {
	if view.scoped() {
		if !view.events {
			return frames{}
		}
		return frames{changes: marshalStateChanges(set.changes, view.scope, set.groups)}
	}
	out := frames{delta: deltaFrame(prev, next, view)}
	if view.events {
		out.events = eventFrames(events, view, next.Groups)
	}
	return out
}

// standaloneEventFrames encodes one event that did not come from a scan
// transition. A standalone event is not a transition — there is no previous
// snapshot to diff — so it can never produce a delta. An unscoped events viewer
// gets the event frame; a scoped viewer gets the redacted summary of the same
// event.
func standaloneEventFrames(ev state.Event, seq uint64, groups []state.Group, set changeSet, view viewSpec) frames {
	if !view.events {
		return frames{}
	}
	if view.scoped() {
		return frames{changes: marshalStateChanges(set.changes, view.scope, set.groups)}
	}
	return frames{events: eventFrames([]state.Event{ev}, view, groups)}
}

// deliver queues a view's frames on the connection that holds the view.
func (f frames) deliver(c *Conn) {
	if f.delta != nil {
		c.enqueue(f.delta)
	}
	for _, m := range f.changes {
		c.enqueue(m)
	}
	for _, m := range f.events {
		c.enqueue(m)
	}
}

// audience is what the connected subscribers collectively asked for, computed
// in one pass so every fan-out decision reads one fact instead of rescanning
// the registry per question.
type audience struct {
	subscribers bool
	scoped      bool
	events      bool
}

// audienceLocked summarizes the registry. Caller holds subsMu.
func (s *Server) audienceLocked() audience {
	var aud audience
	for _, c := range s.conns {
		if !c.subscribed {
			continue
		}
		aud.subscribers = true
		aud.events = aud.events || c.events
		aud.scoped = aud.scoped || !s.effectiveScopeLocked(c.scope).Empty()
	}
	return aud
}

// viewForLocked derives the view a connection is entitled to right now.
// Caller holds subsMu.
func (s *Server) viewForLocked(c *Conn) viewSpec {
	return viewSpec{include: c.include, scope: s.effectiveScopeLocked(c.scope), events: c.events}
}

// publish fans a scanner transition out to subscribers. Scoped connections
// receive only their redacted state.changed summaries; unscoped connections
// keep the historical delta/event view. Each wire view is encoded once.
func (s *Server) publish(prev, next state.Snapshot, events []state.Event) {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	s.recordReplay(prev, next, events)

	aud := s.audienceLocked()
	// Events raised before anyone was listening ride out with the first publish
	// that has an events viewer to carry them to.
	if aud.events {
		events = append(s.takePending(), events...)
	}
	if !aud.subscribers {
		return
	}

	// The change set is only worth building when somebody is watching a scope;
	// a scan tick with no scoped subscriber is the common case.
	var set changeSet
	if aud.scoped {
		set = buildChangeSet(prev, next, events)
	}
	cache := newFrameCache()
	for _, c := range s.conns {
		if !c.subscribed {
			continue
		}
		view := s.viewForLocked(c)
		encoded := cache.forView(view, func() frames {
			return transitionFrames(prev, next, events, set, view)
		})
		encoded.deliver(c)
	}
}

// buildChangeSet is newChangeSet behind a seam, so a test can tell the "nobody
// watches a scope" early exit from the work it skips.
var buildChangeSet = newChangeSet

// BroadcastEvent sends one event to subscribers that asked for events, applying
// the scoped relation before it reaches the wire.
func (s *Server) BroadcastEvent(ev state.Event) { s.broadcastEvent(ev) }

// broadcastEvent sends one standalone event to matching subscribers that asked
// for events. It is the event path, not a transition: subscribers get the event
// (or its redacted summary), never a delta.
func (s *Server) broadcastEvent(ev state.Event) {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()

	snap := s.loop.Cached()
	set := eventChangeSet(ev, snap)
	cache := newFrameCache()
	for _, c := range s.conns {
		if !c.subscribed || !c.events {
			continue
		}
		view := s.viewForLocked(c)
		encoded := cache.forView(view, func() frames {
			return standaloneEventFrames(ev, snap.Seq, snap.Groups, set, view)
		})
		encoded.deliver(c)
	}
}

// deltaFrame builds one state.delta notification for a view. It returns nil
// when the delta is empty for this subscriber, so a client that asked for
// neither stats nor health is not woken by a stats-only tick.
func deltaFrame(prev, next state.Snapshot, view viewSpec) []byte {
	delta, ok := deltaFor(prev, next, view)
	if !ok {
		return nil
	}
	frame, ok := frameNotification(rpc.MethodStateDelta, delta)
	if !ok {
		return nil
	}
	return frame
}

// deltaFor computes the row diff this view may see. Scope selects the rows
// before the diff; include decides which diff is computed (with or without
// stats) and which enrichment survives on the added and updated ports. The
// envelope is not built here — deltaFrame owns that — so field permission and
// wire framing stay two separate decisions.
func deltaFor(prev, next state.Snapshot, view viewSpec) (state.Delta, bool) {
	prev, next = view.scope.FilterSnapshot(prev), view.scope.FilterSnapshot(next)
	delta := state.Diff(prev, next)
	if view.include.Stats {
		delta = state.DiffWithStats(prev, next)
	}
	if emptyDelta(delta) && state.SameObservation(prev.Observation, next.Observation) {
		return state.Delta{}, false
	}
	delta.Ports.Added = filterPorts(delta.Ports.Added, view.include)
	delta.Ports.Updated = filterPorts(delta.Ports.Updated, view.include)
	return delta, true
}

// eventFrames builds one state.event notification per event this view may see.
func eventFrames(events []state.Event, view viewSpec, groups []state.Group) [][]byte {
	visible := visibleEvents(events, view, groups)
	out := make([][]byte, 0, len(visible))
	for _, ev := range visible {
		if frame, ok := frameNotification(rpc.MethodStateEvent, ev); ok {
			out = append(out, frame)
		}
	}
	return out
}

// visibleEvents drops the events outside this view's scope and strips the port
// enrichment the view did not ask for. Each event is copied before its port is
// replaced, so filtering one view cannot mutate the transition another view is
// about to read.
func visibleEvents(events []state.Event, view viewSpec, groups []state.Group) []state.Event {
	out := make([]state.Event, 0, len(events))
	for _, ev := range events {
		if !view.scope.MatchEvent(ev, groups) {
			continue
		}
		if ev.Port != nil {
			port := filterPort(*ev.Port, view.include)
			ev.Port = &port
		}
		out = append(out, ev)
	}
	return out
}

// frameNotification frames one payload as a notification and reports whether it
// encoded. A payload the encoder refuses is dropped rather than sent as a frame
// whose params are absent, so every caller either queues a decodable frame or
// nothing.
func frameNotification(method string, payload any) ([]byte, bool) {
	frame, err := marshalNotification(method, payload)
	if err != nil {
		return nil, false
	}
	return frame, true
}

func marshalStateChanges(changes []state.StateChanged, scope state.Scope, groups []state.Group) [][]byte {
	out := make([][]byte, 0, len(changes))
	for _, change := range changes {
		if !scope.MatchChangedIn(change, groups) {
			continue
		}
		// A change that cannot be encoded is dropped rather than sent as a
		// notification whose params are missing.
		if frame, ok := frameNotification(rpc.MethodStateChanged, change); ok {
			out = append(out, frame)
		}
	}
	return out
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
	// Collections are emptied rather than left null: a client decoding this
	// expects arrays, and null would make every caller branch on it.
	if snap.Groups == nil {
		snap.Groups = []state.Group{}
	}
	if snap.Sessions == nil {
		snap.Sessions = []state.SessionRecord{}
	}
	return snap
}

// filterPorts applies one subscriber's include set to a snapshot's rows. A set
// that asks for everything leaves the slice untouched rather than copying it:
// nothing is stripped, so a shared slice is safe.
func filterPorts(rows []state.Port, include scanner.Include) []state.Port {
	if include.Stats && include.Health {
		return rows
	}
	filtered := make([]state.Port, len(rows))
	for i := range rows {
		filtered[i] = filterPort(rows[i], include)
	}
	return filtered
}

// filterPort strips what this subscriber did not ask for.
//
// Health that an `oberth.yaml` asked the daemon to poll is the exception: the
// config declared it, so it is state every subscriber sees whether or not it
// asked for `include: ["health"]`.
func filterPort(row state.Port, include scanner.Include) state.Port {
	if !include.Stats {
		row.Stats = nil
	}
	if !include.Health && row.Health != nil && !row.Health.Configured {
		row.Health = nil
	}
	return row
}

// ------------------------------------------------------------------- idle ---

// touch records client activity and so resets the idle countdown.
func (s *Server) touch() { s.lastActive.Store(time.Now().UnixNano()) }

// recountKeepalive recomputes the number of connected clients that asked the
// daemon to stay up (daemon.hello{keepalive:true}).
func (s *Server) recountKeepalive() {
	s.keepalives.Store(s.countKeepalive())
}

// countKeepalive is how many connected clients asked the daemon to stay up. It
// is counted rather than derived because each connection may change its mind,
// and the idle watcher reads the number without holding the registry lock.
func (s *Server) countKeepalive() int64 {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	asking := int64(0)
	for _, c := range s.conns {
		if c.Keepalive() {
			asking++
		}
	}
	return asking
}

// idleCheckEvery is how often the idle countdown is polled: a quarter of the
// timeout, so a stop lands within a quarter of the window, but never faster than
// idleCheckInterval and never below a millisecond. A ticker rejects a
// non-positive period, which is what the floor is for.
func (s *Server) idleCheckEvery() time.Duration {
	tick := idleCheckInterval
	if quarter := s.opts.IdleTimeout / 4; quarter < tick {
		tick = quarter
	}
	if tick <= 0 {
		return time.Millisecond
	}
	return tick
}

// watchIdle stops the daemon after IdleTimeout without activity. A keepalive
// client or any subscriber counts as activity, so the desktop app keeps the
// daemon alive just by staying connected.
func (s *Server) watchIdle(ctx context.Context) {
	if s.opts.IdleTimeout <= 0 {
		return
	}
	ticker := time.NewTicker(s.idleCheckEvery())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopping:
			return
		case <-ticker.C:
			if s.watched() {
				s.touch()
				continue
			}
			idle := time.Since(time.Unix(0, s.lastActive.Load()))
			if idle < s.opts.IdleTimeout {
				continue
			}
			s.logger.Info("idle timeout reached, stopping",
				"idle", idle.Round(time.Second).String())
			s.Shutdown()
			return
		}
	}
}

// watched reports whether a keepalive client or a subscriber is still watching,
// which is what keeps an idle daemon up.
func (s *Server) watched() bool {
	return s.keepalives.Load() > 0 || s.Subscribers() > 0
}
