package groups

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// Exercise actual directory walks and manifest attribution, not just an empty
// resolver. This file also runs unchanged against the pinned pre-pass tree.
func BenchmarkAttributionPass(b *testing.B) {
	for _, n := range []int{16, 256, 1024} {
		for _, mode := range []string{"shared", "nested", "distinct", "compose"} {
			b.Run(fmt.Sprintf("rows=%d/%s", n, mode), func(b *testing.B) {
				base := b.TempDir()
				roots := 1
				if mode == "distinct" {
					roots = n
				}
				index := NewIndex()
				rows := make([]ports.ListeningPort, n)
				for g := 0; g < roots; g++ {
					root := filepath.Join(base, fmt.Sprintf("repo-%d", g))
					if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
						b.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, ConfigName), []byte(fmt.Sprintf("name: project-%d\nports: [8080]\n", g)), 0o644); err != nil {
						b.Fatal(err)
					}
					for i := g; i < n; i += roots {
						cwd := root
						if mode == "nested" {
							cwd = filepath.Join(root, fmt.Sprintf("package-%d", i%16), "src")
							if err := os.MkdirAll(cwd, 0o755); err != nil {
								b.Fatal(err)
							}
						}
						rows[i] = ports.ListeningPort{PID: 100 + i, Port: 8080, Cwd: cwd, Process: "server"}
						if mode == "compose" {
							rows[i].Cwd = ""
							rows[i].DockerContainer = "container"
							rows[i].DockerComposeProject = "stack"
							rows[i].DockerComposeWorkingDir = root
						}
					}
				}
				AttributeWith(rows, NoRuns{}, index) // warm file parsing, not path observations
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					AttributeWith(rows, NoRuns{}, index)
				}
			})
		}
	}
}

// Every listener lacks cwd/Compose evidence and declares a different port.
// The pass must not impose large memo tables on the existing indexed path.
func BenchmarkResolutionPathless(b *testing.B) {
	index := NewIndex()
	rows := make([]state.Port, 1024)
	for i := range rows {
		index.Add(&Config{Name: fmt.Sprintf("p%d", i), Dir: filepath.Join(b.TempDir(), "missing"), Ports: []int{3000 + i}})
		rows[i] = state.Port{Port: 3000 + i}
	}
	Resolve(rows, nil, index)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Resolve(rows, nil, index)
	}
}

func BenchmarkAttributionPathless(b *testing.B) {
	index := NewIndex()
	rows := make([]ports.ListeningPort, 1024)
	base := b.TempDir()
	for i := range rows {
		index.Add(&Config{Name: fmt.Sprintf("p%d", i), Dir: filepath.Join(base, fmt.Sprintf("missing%d", i)), Ports: []int{3000 + i}})
		rows[i] = ports.ListeningPort{Port: 3000 + i, PID: 100 + i, Process: "server"}
	}
	AttributeWith(rows, NoRuns{}, index)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		AttributeWith(rows, NoRuns{}, index)
	}
}
