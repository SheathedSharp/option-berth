package scanner

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// The barrier never releases the sampler until the write has completed. This
// proves that a blocked external command cannot occupy the publication gate;
// it is not a benchmark that hopes the scheduler happens to race correctly.
func TestStatsPreparationDoesNotBlockPublication(t *testing.T) {
	for _, mode := range []string{"republish", "bare-rescan", "full-rescan", "reused-pid", "new-listener"} {
		t.Run(mode, func(t *testing.T) {
			entered, release, sampled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			rows := []ports.ListeningPort{{Port: 4000, PID: 42, StartedAt: "2026-01-01T00:00:00Z", MemoryRSS: 100, CPUPercent: 1}}
			l := New(Options{
				Scan: func(Include) ([]ports.ListeningPort, error) { return slices.Clone(rows), nil },
				SampleStats: func([]int) map[int]ports.ProcSample {
					close(entered)
					<-release
					return map[int]ports.ProcSample{42: {MemoryRSS: 900}}
				},
			})
			if _, err := l.Rescan(Include{Stats: true}); err != nil {
				t.Fatal(err)
			}
			before := l.Cached()
			go func() { defer close(sampled); l.sampleStats(Include{Stats: true}) }()
			<-entered
			t.Cleanup(func() { unblock(); <-sampled })
			switch mode {
			case "full-rescan":
				rows[0].MemoryRSS = 333
			case "bare-rescan":
				rows[0].Display = "renamed"
			case "reused-pid":
				rows[0].StartedAt = "2026-01-02T00:00:00Z"
				rows[0].MemoryRSS = 333
			case "new-listener":
				rows = append(rows, ports.ListeningPort{Port: 4001, PID: 42, MemoryRSS: 333, StartedAt: rows[0].StartedAt})
			}
			written := make(chan error, 1)
			go func() {
				if mode == "republish" {
					written <- l.Republish()
					return
				}
				_, err := l.Rescan(Include{Stats: mode == "full-rescan"})
				written <- err
			}()
			select {
			case err := <-written:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				unblock()
				<-written
				t.Fatal("publication waited for the blocked stats collector")
			}
			afterWrite := l.Cached()
			writtenRSS := afterWrite.Ports[0].Stats.MemoryRSS
			unblock()
			<-sampled
			after := l.Cached()
			want := int64(900)
			if mode == "full-rescan" || mode == "reused-pid" {
				want = 333
			}
			if after.Ports[0].Stats.MemoryRSS != want {
				t.Fatalf("stats=%+v, want RSS %d", after.Ports[0].Stats, want)
			}
			if mode == "new-listener" && after.Ports[1].Stats.MemoryRSS != 333 {
				t.Fatal("sample enriched a listener absent from its input")
			}
			if mode == "bare-rescan" && after.Ports[0].DisplayName != "renamed" {
				t.Fatal("sample reverted the current identity row")
			}
			if mode == "full-rescan" && after.Seq != afterWrite.Seq {
				t.Fatal("superseded sample consumed a sequence")
			}
			if before.Ports[0].Stats.MemoryRSS != 100 || afterWrite.Ports[0].Stats.MemoryRSS != writtenRSS {
				t.Fatal("retained reader snapshot mutated")
			}
		})
	}
}

func TestStatsRebaseRejectsReplacedRunAndContainer(t *testing.T) {
	old := []state.Port{{Port: 4000, PID: 42, Stats: &state.Stats{MemoryRSS: 100}, Run: &state.Run{ID: "before"}}}
	for _, change := range []func(*state.Port){
		func(p *state.Port) { p.Run = &state.Run{ID: "after"} },
		func(p *state.Port) { p.Docker = &state.Docker{Container: "replacement"} },
	} {
		current := slices.Clone(old)
		change(&current[0])
		next := refreshStatsFrom(current, map[int]ports.ProcSample{42: {MemoryRSS: 900}}, old, true)
		if next[0].Stats.MemoryRSS != 100 {
			t.Fatal("rebased sample crossed a runtime boundary")
		}
	}
}
