package client

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func deltaMessage() rpc.Message {
	return rpc.Message{Method: rpc.MethodStateDelta, Params: json.RawMessage(`{"seq":1}`)}
}

func eventMessage() rpc.Message {
	return rpc.Message{Method: rpc.MethodStateEvent, Params: json.RawMessage(`{"kind":"port_up"}`)}
}

// Delivery and Unsubscribe may race on the connection: a notification must
// never be sent on a channel that close() has just closed.
func TestDeliverSurvivesAConcurrentClose(t *testing.T) {
	for round := 0; round < 200; round++ {
		subscription := &Subscription{
			deltas: make(chan state.Delta, 1),
			events: make(chan state.Event, 1),
		}

		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			defer func() {
				if problem := recover(); problem != nil {
					t.Errorf("deliver panicked against close: %v", problem)
				}
			}()
			for i := 0; i < 50; i++ {
				subscription.deliver(deltaMessage())
				subscription.deliver(eventMessage())
			}
		}()
		go func() {
			defer group.Done()
			subscription.close()
		}()
		group.Wait()

		// close is idempotent; delivery afterwards is quiet.
		subscription.close()
		subscription.deliver(deltaMessage())
	}
}

func TestDeliverDropsInsteadOfBlocking(t *testing.T) {
	subscription := &Subscription{deltas: make(chan state.Delta, 1)}

	subscription.deliver(deltaMessage())
	if subscription.Dropped() {
		t.Fatal("a delivery into free space counted as dropped")
	}
	if buffered := len(subscription.deltas); buffered != 1 {
		t.Fatalf("buffered = %d, want 1", buffered)
	}

	subscription.deliver(deltaMessage())
	if !subscription.Dropped() {
		t.Fatal("a delivery into a full buffer did not set Dropped")
	}
}

func TestDeliverAfterCloseIsQuiet(t *testing.T) {
	subscription := &Subscription{
		deltas: make(chan state.Delta, 1),
		events: make(chan state.Event, 1),
	}
	subscription.close()

	subscription.deliver(deltaMessage())
	subscription.deliver(eventMessage())
	if subscription.Dropped() {
		t.Fatal("a notification after close counted as dropped")
	}
}
