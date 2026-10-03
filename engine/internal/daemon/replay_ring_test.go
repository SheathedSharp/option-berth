package daemon

import (
	"github.com/sheathedsharp/option-berth/internal/state"
	"testing"
)

func TestReplayWindowWrapsAndDetachesReaders(t *testing.T) {
	s := &Server{}
	for seq := uint64(1); seq <= stateReplayCapacity*3+7; seq++ {
		s.recordReplay(state.Snapshot{Seq: seq - 1}, state.Snapshot{Seq: seq}, []state.Event{{Seq: seq, Kind: "observed"}})
		if len(s.replay) > stateReplayCapacity {
			t.Fatal("unbounded replay retention")
		}
		oldest := uint64(0)
		if seq > stateReplayCapacity {
			oldest = seq - stateReplayCapacity
		}
		// after_seq=0 always means full sync, not replay from an initial cursor.
		for _, cursor := range []uint64{max(uint64(1), oldest), max(uint64(1), oldest+1), max(uint64(1), (oldest+seq)/2), max(uint64(1), seq-1), seq} {
			if cursor > seq {
				continue
			}
			base, items, ok := s.replayFrom(cursor, state.Snapshot{Seq: seq})
			if !ok || base.Seq != cursor || len(items) != int(seq-cursor) {
				t.Fatalf("seq=%d cursor=%d: base=%d items=%d resumed=%v", seq, cursor, base.Seq, len(items), ok)
			}
			for i, item := range items {
				expected := cursor + uint64(i)
				if item.prev.Seq != expected || item.next.Seq != expected+1 || item.events[0].Seq != expected+1 {
					t.Fatal("wrapped replay lost chronology or events")
				}
			}
		}
		if oldest > 1 {
			if _, _, ok := s.replayFrom(oldest-1, state.Snapshot{Seq: seq}); ok {
				t.Fatal("evicted cursor resumed")
			}
		}
	}
	current := uint64(stateReplayCapacity*3 + 7)
	_, held, ok := s.replayFrom(current-3, state.Snapshot{Seq: current})
	if !ok {
		t.Fatal("fixture did not resume")
	}
	saved := held[0].next.Seq
	for seq := current + 1; seq <= current+stateReplayCapacity+1; seq++ {
		s.recordReplay(state.Snapshot{Seq: seq - 1}, state.Snapshot{Seq: seq}, nil)
	}
	if held[0].next.Seq != saved {
		t.Fatal("overwriting window mutated retained replay reader")
	}
	for _, slot := range s.replay {
		if slot.next.Seq <= current {
			t.Fatal("evicted snapshot still retained in live slot")
		}
	}
}

func TestReplayWindowRejectsGapsAfterWrap(t *testing.T) {
	s := &Server{}
	for seq := uint64(1); seq <= stateReplayCapacity+3; seq++ {
		s.recordReplay(state.Snapshot{Seq: seq - 1}, state.Snapshot{Seq: seq}, nil)
	}
	s.recordReplay(state.Snapshot{Seq: stateReplayCapacity + 4}, state.Snapshot{Seq: stateReplayCapacity + 5}, nil)
	current := state.Snapshot{Seq: stateReplayCapacity + 5}
	if base, items, ok := s.replayFrom(stateReplayCapacity+2, current); ok || len(items) != 0 || base.Seq != current.Seq {
		t.Fatal("partial chain presented as a complete replay")
	}
}

func TestReplaySteadyStateDoesNotCopyWindow(t *testing.T) {
	s := &Server{}
	seq := uint64(1)
	for ; seq <= stateReplayCapacity; seq++ {
		s.recordReplay(state.Snapshot{Seq: seq - 1}, state.Snapshot{Seq: seq}, nil)
	}
	if got := testing.AllocsPerRun(100, func() { s.recordReplay(state.Snapshot{Seq: seq - 1}, state.Snapshot{Seq: seq}, nil); seq++ }); got != 0 {
		t.Fatalf("steady publication allocated %g times", got)
	}
}

func BenchmarkReplayPublication(b *testing.B) {
	s := &Server{}
	seq := uint64(1)
	for ; seq <= stateReplayCapacity; seq++ {
		s.recordReplay(state.Snapshot{Seq: seq - 1}, state.Snapshot{Seq: seq}, nil)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.recordReplay(state.Snapshot{Seq: seq - 1}, state.Snapshot{Seq: seq}, nil)
		seq++
	}
}
