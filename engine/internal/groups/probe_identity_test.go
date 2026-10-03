package groups

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Rename an equally long directory entry and restore its timestamp. This
// produces identical directory metadata but different manifest presence,
// without sleeping, changing an assertion, or mutating the index's internals.
func TestNegativeProbeObservesUnchangedDirectoryStamp(t *testing.T) {
	for _, name := range configNames {
		t.Run(name, func(t *testing.T) {
			dir := tempTree(t)
			placeholder := filepath.Join(dir, strings.Repeat("x", len(name)))
			writeFile(t, placeholder, "name: discovered\nservices:\n  - name: api\n    port: 8080\n")
			before, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			index := NewIndex()
			index.Observe(dir)
			if index.At(dir) != nil {
				t.Fatal("fixture already had a recognized manifest")
			}
			if err := os.Rename(placeholder, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(dir, before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
				t.Fatalf("fixture stamp moved: before=%v/%d after=%v/%d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
			}
			index.Observe(dir)
			cfg := index.At(dir)
			if cfg == nil || cfg.Name != "discovered" {
				t.Fatalf("new manifest hidden by an unchanged directory stamp: %+v", cfg)
			}
			if candidate, _, ok := index.MatchPort(nativePort(8080, dir)); !ok || candidate != cfg {
				t.Fatal("discovered manifest did not invalidate the port claim index")
			}
		})
	}
}

// Measure the correctness tradeoff as well as the Git improvements. The
// absent case rechecks a fixed set of names; do not disguise that extra work
// as a performance gain. Positive files keep the existing reuse path.
func BenchmarkManifestProbeRound(b *testing.B) {
	for _, presence := range []string{"absent", "present"} {
		b.Run(presence, func(b *testing.B) {
			dir := b.TempDir()
			if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
				b.Fatal(err)
			}
			if presence == "present" {
				if err := os.WriteFile(filepath.Join(dir, ConfigName), []byte("name: benchmark\n"), 0o600); err != nil {
					b.Fatal(err)
				}
			}
			index := NewIndex()
			index.Observe(dir)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				index.Observe(dir)
			}
		})
	}
}
