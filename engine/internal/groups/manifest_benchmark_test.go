package groups

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

// Include the real filesystem reads. The one-edit workload excludes fixture
// writes but not discovery, content comparison, parsing or index maintenance.
func BenchmarkManifestReconcile(b *testing.B) {
	for _, n := range []int{16, 256} {
		for _, mode := range []string{"unchanged", "one-edit"} {
			b.Run(fmt.Sprintf("roots=%d/%s", n, mode), func(b *testing.B) {
				root := b.TempDir()
				dirs := make([]string, n)
				for i := range dirs {
					dirs[i] = filepath.Join(root, fmt.Sprint(i))
					if err := os.Mkdir(dirs[i], 0o700); err != nil {
						b.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dirs[i], ConfigName),
						[]byte("name: project\nservices:\n  - name: api\n    port: 8080\n"), 0o600); err != nil {
						b.Fatal(err)
					}
				}
				x := NewIndex()
				x.Reload(dirs)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if mode == "one-edit" {
						b.StopTimer()
						body := fmt.Sprintf("name: project\nservices:\n  - name: api\n    port: %d\n", 8081+i%2)
						if err := os.WriteFile(filepath.Join(dirs[0], ConfigName), []byte(body), 0o600); err != nil {
							b.Fatal(err)
						}
						b.StartTimer()
					}
					x.Reload(nil)
				}
			})
		}
	}
}

// AttributeWith itself now refreshes known manifests even without listeners.
func BenchmarkManifestPublication(b *testing.B) {
	for _, n := range []int{16, 1024} {
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			root := b.TempDir()
			if err := os.WriteFile(filepath.Join(root, ConfigName), []byte("name: bench\nports: [8080]\n"), 0o600); err != nil {
				b.Fatal(err)
			}
			x := NewIndex()
			pp := make([]ports.ListeningPort, n)
			for i := range pp {
				pp[i] = ports.ListeningPort{Port: 8080, PID: 100 + i, Cwd: root}
			}
			AttributeWith(pp, NoRuns{}, x)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				AttributeWith(pp, NoRuns{}, x)
			}
		})
	}
}
