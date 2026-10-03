package scanner

import (
	"fmt"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

// BenchmarkSnapshot measures the scanner's attribution and publication path
// with the OS scan replaced by a fixed fixture. It makes the cost of carrying
// 16..1024 listeners through the daemon visible without folding lsof, ps or
// Docker latency into the algorithm result.
func BenchmarkSnapshot(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("listeners=%d", n), func(b *testing.B) {
			rows := make([]ports.ListeningPort, n)
			for i := range rows {
				rows[i] = ports.ListeningPort{
					Port: 3000 + i, PID: i + 1, Process: "worker",
					Command: "worker --port", Cwd: b.TempDir(),
				}
			}
			l := New(Options{
				DaemonVersion: "bench",
				Scan: func(Include) ([]ports.ListeningPort, error) {
					out := make([]ports.ListeningPort, len(rows))
					copy(out, rows)
					return out, nil
				},
			})
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				l.Invalidate()
				if _, err := l.Snapshot(Include{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
