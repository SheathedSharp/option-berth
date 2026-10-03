package scanner

import (
	"fmt"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

func BenchmarkStatsTick(b *testing.B) {
	for _, n := range []int{16, 1024} {
		for _, mode := range []string{"idle", "sparse", "dense"} {
			b.Run(fmt.Sprintf("rows=%d/%s", n, mode), func(b *testing.B) {
				rows := make([]ports.ListeningPort, n)
				samples := make(map[int]ports.ProcSample, n)
				for i := range rows {
					rows[i] = ports.ListeningPort{PID: 100 + i, Port: 3000 + i, MemoryRSS: 1024, CPUPercent: 1}
					samples[100+i] = ports.ProcSample{MemoryRSS: 1024, CPUPercent: 1}
				}
				l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) { return rows, nil }, SampleStats: func([]int) map[int]ports.ProcSample { return samples }})
				if _, err := l.Rescan(Include{Stats: true}); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					switch mode {
					case "sparse":
						s := samples[100]
						s.MemoryRSS++
						samples[100] = s
					case "dense":
						for pid, s := range samples {
							s.MemoryRSS++
							samples[pid] = s
						}
					}
					l.sampleStats(Include{Stats: true})
				}
			})
		}
	}
}
