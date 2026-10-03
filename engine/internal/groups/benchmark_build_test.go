package groups

import (
	"fmt"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

// BenchmarkGroupsWith measures the manifest-to-runtime join independently of
// scanning. This used to scan every member for every service, which is
// quadratic when a worktree exposes many listeners.
func BenchmarkGroupsWith(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("services=%d", n), func(b *testing.B) {
			cfg := &Config{Name: "bench", Dir: "/tmp/oberth-bench", Services: make([]Service, n)}
			group := "bench"
			groupSource := state.SourceFile
			ports := make([]state.Port, n)
			for i := 0; i < n; i++ {
				cfg.Services[i] = Service{Name: fmt.Sprintf("service-%d", i), Port: 3000 + i}
				ports[i] = state.Port{Port: 3000 + i, Group: &group, GroupSource: &groupSource, PID: i + 1}
			}
			index := NewIndex()
			index.Add(cfg)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = GroupsWith(ports, index, nil)
			}
		})
	}
}
