package groups

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

// BenchmarkIndexMatchPort measures the hot attribution lookup without any OS
// work. The sizes model a small project, a normal multi-worktree checkout and
// a deliberately large daemon index.
func BenchmarkIndexMatchPort(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("configs=%d", n), func(b *testing.B) {
			index := NewIndex()
			ports := make([]state.Port, n)
			for i := 0; i < n; i++ {
				index.Add(&Config{
					Name: fmt.Sprintf("project-%d", i),
					Dir:  fmt.Sprintf("/tmp/oberth-bench/project-%d", i),
					Services: []Service{{
						Name: fmt.Sprintf("service-%d", i),
						Port: 3000 + i,
					}},
				})
				ports[i] = state.Port{Port: 3000 + i}
			}
			// Build any lazy index outside the timed loop.
			_, _, _ = index.MatchPort(ports[0])
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, _ = index.MatchPort(ports[i%len(ports)])
			}
		})
	}
}

// BenchmarkResolve measures the complete deterministic group attribution
// pass after the scan rows already exist.
func BenchmarkResolve(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("ports=%d", n), func(b *testing.B) {
			index := NewIndex()
			ports := make([]state.Port, n)
			for i := 0; i < n; i++ {
				index.Add(&Config{
					Name: fmt.Sprintf("project-%d", i),
					Dir:  fmt.Sprintf("/tmp/oberth-bench/project-%d", i),
					Services: []Service{{
						Name: fmt.Sprintf("service-%d", i),
						Port: 3000 + i,
					}},
				})
				ports[i] = state.Port{Port: 3000 + i}
			}
			_, _, _ = index.MatchPort(ports[0])
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = Resolve(ports, NoRuns{}, index)
			}
		})
	}
}

// BenchmarkResolveSharedCwd isolates the filesystem discovery cache used when
// many listeners belong to one checkout. A real project commonly has one cwd
// for every native service, so this is a separate workload from the synthetic
// no-cwd attribution benchmark above.
func BenchmarkResolveSharedCwd(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("ports=%d", n), func(b *testing.B) {
			root := b.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
				b.Fatal(err)
			}
			index := NewIndex()
			cfg := &Config{Name: "bench", Dir: root, Services: make([]Service, n)}
			ports := make([]state.Port, n)
			for i := range cfg.Services {
				cfg.Services[i] = Service{Name: fmt.Sprintf("service-%d", i), Port: 3000 + i}
				ports[i] = state.Port{Port: 3000 + i, Cwd: root}
			}
			index.Add(cfg)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = Resolve(ports, NoRuns{}, index)
			}
		})
	}
}
