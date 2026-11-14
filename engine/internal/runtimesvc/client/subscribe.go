package client

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// SubscribeOptions mirrors state.subscribe's parameters.
type SubscribeOptions struct {
	// Stats and Health map onto the wire's include list.
	Stats  bool
	Health bool

	// Events asks for notifications. An unscoped subscription receives legacy
	// state.event values; a scoped one receives state.changed on Changes.
	Events bool

	// AfterSeq resumes after the last sequence the caller applied. Zero asks
	// for the historical snapshot-only behaviour; an already-evicted sequence
	// yields a fresh snapshot.
	AfterSeq uint64

	// Scope limits the snapshot and notifications to selected worktrees,
	// repositories or an ephemeral workspace. Nil keeps the daemon-wide view.
	Scope *state.Scope

	// Buffer sizes the delta and event channels. Zero means 64.
	Buffer int
}

func (o SubscribeOptions) include() rpc.Include {
	var include rpc.Include
	if o.Stats {
		include = append(include, "stats")
	}
	if o.Health {
		include = append(include, "health")
	}
	return include
}

// Subscription is a live view of daemon state: the snapshot that opened it and
// the notification channels. The channels close when the subscription is
// cancelled or the connection drops.
type Subscription struct {
	// Snapshot is the full state at the moment an unscoped subscription was
	// made; its first delta on Deltas continues from Seq.
	Snapshot state.Snapshot

	// StreamSnapshot is the redacted opening snapshot of a scoped
	// subscription.
	StreamSnapshot state.StreamSnapshot

	// Deltas carries state.delta notifications.
	Deltas <-chan state.Delta

	// Events carries legacy state.event notifications; nil unless an unscoped
	// subscription asked for them.
	Events <-chan state.Event

	// Changes carries redacted state.changed notifications for scoped
	// streams.
	Changes <-chan state.StateChanged

	client  *Client
	deltas  chan state.Delta
	events  chan state.Event
	changes chan state.StateChanged

	// mu makes delivery and close mutually exclusive: the read loop must not
	// send on a channel that Unsubscribe or a dropped connection is closing.
	mu      sync.Mutex
	shut    bool
	dropped bool
}

// Subscribe registers for state deltas. The returned Subscription carries the
// opening snapshot and the delta and event channels.
func (c *Client) Subscribe(ctx context.Context, opts SubscribeOptions) (*Subscription, error) {
	subscription := newSubscription(c, opts, channelBuffer(opts.Buffer))

	// Register before calling, so a delta that arrives between the daemon
	// queuing the snapshot and this client reading the reply is not lost.
	c.mu.Lock()
	c.subs = append(c.subs, subscription)
	c.mu.Unlock()

	var result rpc.StateSnapshotResult
	err := c.Call(ctx, "state.subscribe", rpc.StateSubscribeParams{
		Include:  opts.include(),
		Events:   opts.Events,
		AfterSeq: opts.AfterSeq,
		Scope:    opts.Scope,
	}, &result)
	if err != nil {
		subscription.detach()
		subscription.close()
		return nil, err
	}
	if result.Type == "state.snapshot" || len(result.Worktrees) > 0 || result.StateRevision != "" {
		subscription.StreamSnapshot = result.Stream()
	} else {
		subscription.Snapshot = result.Legacy()
	}
	return subscription, nil
}

func channelBuffer(requested int) int {
	if requested > 0 {
		return requested
	}
	return 64
}

func newSubscription(c *Client, opts SubscribeOptions, buffer int) *Subscription {
	subscription := &Subscription{
		client: c,
		deltas: make(chan state.Delta, buffer),
	}
	subscription.Deltas = subscription.deltas

	scoped := opts.Scope != nil && !opts.Scope.Empty()
	switch {
	case opts.Events && scoped:
		subscription.changes = make(chan state.StateChanged, buffer)
		subscription.Changes = subscription.changes
	case opts.Events:
		subscription.events = make(chan state.Event, buffer)
		subscription.Events = subscription.events
	}
	return subscription
}

// Unsubscribe stops the subscription and closes its channels.
func (s *Subscription) Unsubscribe(ctx context.Context) error {
	err := s.client.Call(ctx, "state.unsubscribe", rpc.Empty{}, nil)
	s.detach()
	s.close()
	return err
}

// Dropped reports whether notifications were lost because the consumer stopped
// reading its channels.
func (s *Subscription) Dropped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

func (s *Subscription) detach() {
	s.client.mu.Lock()
	kept := s.client.subs[:0]
	for _, other := range s.client.subs {
		if other != s {
			kept = append(kept, other)
		}
	}
	s.client.subs = kept
	s.client.mu.Unlock()
}

func (s *Subscription) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shut {
		return
	}
	s.shut = true
	close(s.deltas)
	if s.events != nil {
		close(s.events)
	}
	if s.changes != nil {
		close(s.changes)
	}
}

// deliver routes one notification onto the channel it belongs to. A full
// channel drops the message and sets dropped rather than blocking the read
// loop, which would stall every other call on the connection. The send holds
// s.mu so it cannot race a concurrent close; it is non-blocking, so holding
// the lock cannot stall the loop either.
func (s *Subscription) deliver(msg rpc.Message) {
	switch msg.Method {
	case rpc.MethodStateDelta:
		deliverTo(s, msg.Params, s.deltas)
	case rpc.MethodStateEvent:
		deliverTo(s, msg.Params, s.events)
	case rpc.MethodStateChanged:
		deliverTo(s, msg.Params, s.changes)
	}
}

// deliverTo decodes one notification and offers it to out. A nil out means the
// subscription did not ask for that kind.
func deliverTo[T any](s *Subscription, raw json.RawMessage, out chan T) {
	if out == nil {
		return
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shut {
		return
	}
	select {
	case out <- value:
	default:
		s.dropped = true
	}
}
