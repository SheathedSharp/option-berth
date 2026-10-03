package scanner

import (
	"fmt"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func BenchmarkSnapshotChangedStats(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("ports=%d", n), func(b *testing.B) {
			prev := state.Snapshot{Ports: make([]state.Port, n)}
			for i := range prev.Ports {
				prev.Ports[i] = state.Port{Host: "localhost", Port: 3000 + i, PID: i + 2, Stats: &state.Stats{MemoryRSS: 1024}}
			}
			next := prev
			next.Ports = append([]state.Port(nil), prev.Ports...)
			next.Ports[n-1].Stats = &state.Stats{MemoryRSS: 2048}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !snapshotChanged(prev, next, true) {
					b.Fatal("stats change disappeared")
				}
			}
		})
	}
}

func BenchmarkRepeatedReadyTimeouts(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("services=%d", n), func(b *testing.B) {
			g := state.Group{Name: "demo", Services: make([]state.Service, n)}
			for i := range g.Services {
				g.Services[i] = state.Service{Name: fmt.Sprintf("worker-%d", i), LastExit: &state.ServiceExit{Reason: "ready_timeout", RunID: fmt.Sprintf("run-%d", i)}}
			}
			prev := state.Snapshot{Seq: 1, Groups: []state.Group{g}}
			next := state.Snapshot{Seq: 2, Groups: []state.Group{g}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if events := deriveEvents(prev, next, "now"); len(events) != 0 {
					b.Fatal("unchanged timeout emitted twice")
				}
			}
		})
	}
}
