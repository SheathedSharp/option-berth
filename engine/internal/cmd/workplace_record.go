package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/workplace"
)

// Recording the work a command started.
//
// `oberth start` and `oberth agent stream` are the two places where work
// happens under this engine's own eyes, so they are where the workplace record
// fills itself in. Everything here is best effort by design: the command's job
// is to run a command or to bridge an agent, and a record that cannot be
// written is a diagnostic, never a failure of that job.
//
// Two rules keep the recording honest:
//
//   - It never starts a daemon. Spawning a background process as a side effect
//     of running a command would be a surprise, and the record is not worth
//     one. No daemon means no record, said out loud once.
//   - It never blocks the thing it records. Events are queued and written by a
//     worker; a full queue drops the event and says so, because a record that
//     quietly misses work reads exactly like a complete one.

const (
	// recordTimeout bounds one recording call. It is short on purpose: the
	// record is bookkeeping beside work someone is waiting on.
	recordTimeout = 3 * time.Second
	// recordQueueDepth is how many events may be waiting to be written. A
	// bridge emits a handful per turn, so this is far more than a healthy
	// daemon needs and small enough that a wedged one cannot grow it.
	recordQueueDepth = 64
)

// workplaceRecorder writes work into the active workplace of one worktree.
type workplaceRecorder struct {
	root   string
	prefix string
	events chan workplace.Record
	stop   chan struct{}
	done   chan struct{}
	closed atomic.Bool
	once   sync.Once
}

// startRecording returns a recorder for the worktree dir sits in, or nil when
// it sits in no worktree. A directory outside every checkout has no workplace
// to record into, which is not an error: the low-level commands work anywhere.
func startRecording(dir string) *workplaceRecorder {
	root, err := resolveWorktree(dir)
	if err != nil {
		return nil
	}
	r := &workplaceRecorder{
		root:   root,
		prefix: workplace.NewID(),
		events: make(chan workplace.Record, recordQueueDepth),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go r.work()
	return r
}

// Observe queues one observation. It never blocks and never fails.
func (r *workplaceRecorder) Observe(rec workplace.Record) {
	if r == nil || r.closed.Load() {
		return
	}
	select {
	case r.events <- rec:
	default:
		r.note("workplace recording is behind; this observation was not recorded")
	}
}

// Close drains what is queued and stops the worker. It is idempotent, so a
// command that exits through os.Exit can close on its way out and still have a
// deferred close run.
func (r *workplaceRecorder) Close() {
	if r == nil || !r.closed.CompareAndSwap(false, true) {
		return
	}
	close(r.stop)
	select {
	case <-r.done:
	case <-time.After(recordTimeout + time.Second):
		r.note("workplace recording did not finish before this command exited")
	}
}

func (r *workplaceRecorder) work() {
	defer close(r.done)
	// The dial is deliberately `dialDaemon`: a daemon that is not running is
	// not started here.
	c, err := dialDaemon(context.Background())
	if err != nil {
		r.note("workplace recording is off: no daemon is listening")
		drain(r.events, r.stop)
		return
	}
	defer c.Close()
	for {
		select {
		case rec := <-r.events:
			r.write(c, rec)
		case <-r.stop:
			for {
				select {
				case rec := <-r.events:
					r.write(c, rec)
				default:
					return
				}
			}
		}
	}
}

func (r *workplaceRecorder) write(c *client.Client, rec workplace.Record) {
	ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
	defer cancel()
	if err := recordInto(ctx, c, r.root, rec); err != nil {
		r.note("workplace recording: " + err.Error())
	}
}

// note says something once. Repeating it per event would bury the command's
// own output in a record that is already failing.
func (r *workplaceRecorder) note(msg string) {
	r.once.Do(func() {
		fmt.Fprintln(os.Stderr, display.Dim("note: "+msg))
	})
}

// drain consumes queued events so a producer never blocks on a worker that has
// given up.
func drain(events <-chan workplace.Record, stop <-chan struct{}) {
	for {
		select {
		case <-events:
		case <-stop:
			return
		}
	}
}

// recordInto writes one block into the worktree's active workplace.
func recordInto(ctx context.Context, c *client.Client, root string, rec workplace.Record) error {
	params := rpc.WorkplaceRecordParams{
		Worktree: root,
		Block: rpc.WorkplaceBlockInput{
			ID: rec.ID, Kind: string(rec.Kind), Source: string(rec.Source), Input: rec.Input,
			Status: string(rec.Status), Provider: rec.Provider, RunID: rec.RunID, PID: rec.PID,
			LogPath: rec.LogPath, Output: rec.Output, ExitCode: rec.ExitCode,
		},
	}
	if !rec.StartedAt.IsZero() {
		params.Block.StartedAt = rec.StartedAt.UTC().Format(time.RFC3339)
	}
	if !rec.EndedAt.IsZero() {
		params.Block.EndedAt = rec.EndedAt.UTC().Format(time.RFC3339)
	}
	var out rpc.WorkplaceRecordResult
	return c.Call(ctx, "workplace.record", params, &out)
}

// startShell records a command that has just been started, and returns the
// block id its end is reported under. It is the caller's whole involvement
// with the record: the id is the only thing it has to carry.
func (r *workplaceRecorder) startShell(argv []string, runID string, pid int, logPath string, started time.Time) string {
	if r == nil {
		return ""
	}
	id := workplace.NewID()
	r.Observe(workplace.Record{
		ID: id, Kind: workplace.KindShell, Source: workplace.SourceEngine,
		Input: strings.Join(argv, " "), Status: workplace.StatusRunning,
		RunID: runID, PID: pid, LogPath: logPath, StartedAt: started,
	})
	return id
}

// endShell records how that command ended.
//
// Only the outcome is sent. The command line, the run identity and the log
// path are already recorded, and re-sending them would be a second copy of
// them to keep in step.
func (r *workplaceRecorder) endShell(blockID string, code int, interrupted bool) {
	if r == nil || blockID == "" {
		return
	}
	exit := code
	r.Observe(workplace.Record{
		ID: blockID, Kind: workplace.KindShell, Source: workplace.SourceEngine,
		Status: workplace.StatusForExit(code, interrupted), ExitCode: &exit,
		EndedAt: time.Now().UTC(),
	})
}

// bridgeEvents turns the structured bridge's own events into workplace
// records: one block per turn, opened when the turn starts and closed when the
// provider says how it ended.
//
// The block id is this bridge process's random prefix plus the turn id. The
// provider's session id is deliberately not used: resuming a session in a new
// bridge would then reuse turn ids and merge two runs of work into one block.
func (r *workplaceRecorder) bridgeEvents(provider string) func(map[string]any) {
	if r == nil {
		// No worktree to record into: the bridge runs with no observer at all
		// rather than with one that drops everything.
		return nil
	}
	return func(event map[string]any) {
		turn, _ := event["turn"].(string)
		if turn == "" {
			return
		}
		id := r.prefix + ":" + turn
		switch event["type"] {
		case "turn.started":
			text, _ := event["input"].(string)
			r.Observe(workplace.Record{
				ID: id, Kind: workplace.KindAgent, Source: workplace.SourceEngine,
				Input: text, Status: workplace.StatusRunning, Provider: provider,
				StartedAt: time.Now().UTC(),
			})
		case "turn.completed":
			outcome, _ := event["outcome"].(string)
			// The prompt is not repeated here: the bridge knows it, but
			// re-sending it would be a second copy of the same text to keep in
			// step. The provider is repeated so an end that arrives on its own
			// still says which agent ran the turn.
			r.Observe(workplace.Record{
				ID: id, Kind: workplace.KindAgent, Source: workplace.SourceEngine,
				Status: workplace.StatusForOutcome(outcome), Provider: provider,
				EndedAt: time.Now().UTC(),
			})
		}
	}
}
