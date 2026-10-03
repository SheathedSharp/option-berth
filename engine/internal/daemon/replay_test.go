package daemon

import (
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestReplayFromReturnsContiguousTransitions(t *testing.T) {
	s := &Server{}
	s.recordReplay(state.Snapshot{Seq: 1}, state.Snapshot{Seq: 2}, nil)
	s.recordReplay(state.Snapshot{Seq: 2}, state.Snapshot{Seq: 3}, []state.Event{{Kind: "port_up"}})

	base, replay, resumed := s.replayFrom(1, state.Snapshot{Seq: 3})
	if !resumed || base.Seq != 1 || len(replay) != 2 {
		t.Fatalf("replay = base %d, %d items, resumed %v; want seq 1 and two items", base.Seq, len(replay), resumed)
	}
	if replay[1].events[0].Kind != "port_up" {
		t.Fatalf("replayed events = %+v, want port_up", replay[1].events)
	}
}

func TestReplayFromFallsBackWhenCursorIsEvicted(t *testing.T) {
	s := &Server{}
	for seq := uint64(1); seq <= stateReplayCapacity+2; seq++ {
		s.recordReplay(state.Snapshot{Seq: seq - 1}, state.Snapshot{Seq: seq}, nil)
	}
	current := state.Snapshot{Seq: stateReplayCapacity + 2}
	base, replay, resumed := s.replayFrom(1, current)
	if resumed || base.Seq != current.Seq || len(replay) != 0 {
		t.Fatalf("evicted replay = base %d, %d items, resumed %v; want current snapshot only", base.Seq, len(replay), resumed)
	}
}
