package runtimesvc

import (
	"encoding/json"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// A verdict that resolves with no row moving must still reach subscribers.
// The scanner publishes the transition; this is the fan-out half that used to
// drop it because the delta had no rows.
func TestObservationOnlyTransitionReachesSubscribers(t *testing.T) {
	rows := []state.Port{{Host: "localhost", Port: 18090, BindAddress: "127.0.0.1", PID: 10}}
	prev := state.Snapshot{Seq: 1, Ports: rows, Observation: &state.Observation{
		State: state.ObservationPartial, Code: "observation_incomplete",
	}}
	next := state.Snapshot{Seq: 2, Ports: rows}

	raw := deltaFrame(prev, next, viewSpec{})
	if raw == nil {
		t.Fatal("the fan-out dropped an observation-only transition")
	}
	var msg rpc.Notification
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("notification: %v", err)
	}
	var delta state.Delta
	if err := json.Unmarshal(msg.Params, &delta); err != nil {
		t.Fatalf("delta: %v", err)
	}
	if delta.Observation != nil {
		t.Fatalf("observation = %+v, want a nil verdict after recovery", delta.Observation)
	}

	// The explicit null is what tells a client "observable now"; an absent
	// field would mean "unchanged" and leave the stale claim in place.
	var envelope struct {
		Params struct {
			Observation json.RawMessage `json:"observation"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if string(envelope.Params.Observation) != "null" {
		t.Fatalf("observation field = %s, want an explicit null", envelope.Params.Observation)
	}
}

func TestUnchangedObservationAndNoRowsStaySilent(t *testing.T) {
	rows := []state.Port{{Host: "localhost", Port: 18090, BindAddress: "127.0.0.1", PID: 10}}
	verdict := &state.Observation{State: state.ObservationBlind, CheckedAt: "2026-01-01T00:00:00Z"}
	prev := state.Snapshot{Seq: 1, Ports: rows, Observation: verdict}
	next := state.Snapshot{Seq: 2, Ports: rows, Observation: &state.Observation{
		State: state.ObservationBlind, CheckedAt: "2026-01-01T00:00:05Z",
	}}
	if raw := deltaFrame(prev, next, viewSpec{}); raw != nil {
		t.Fatalf("re-checked identical verdict produced a frame: %s", raw)
	}
}
