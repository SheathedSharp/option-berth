package state

import (
	"fmt"
	"testing"
)

// BenchmarkDiff measures the keyed snapshot delta independently of process
// scanning. It makes regressions in the event path visible as the number of
// listeners and groups grows.
func BenchmarkDiff(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("ports=%d", n), func(b *testing.B) {
			prev := Snapshot{Ports: make([]Port, n)}
			next := Snapshot{Ports: make([]Port, n)}
			for i := 0; i < n; i++ {
				prev.Ports[i] = Port{Host: "localhost", Port: 3000 + i, BindAddress: "127.0.0.1", PID: i + 1}
				next.Ports[i] = prev.Ports[i]
			}
			if n > 0 {
				next.Ports[n-1].PID++
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = Diff(prev, next)
			}
		})
	}
}
