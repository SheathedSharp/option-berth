package killer

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// referenceDescendants deliberately retains the old independent scan-based
// traversal to pin ordering, depth limits, cycles and missing-root behavior.
func referenceDescendants(table ProcessTable, root int) []int {
	if root <= 0 {
		return nil
	}
	seen := map[int]bool{}
	var walk func(int, int) []int
	walk = func(pid, depth int) []int {
		if seen[pid] || depth > maxTreeDepth {
			return nil
		}
		seen[pid] = true
		var out []int
		for _, child := range table.Children(pid) {
			out = append(out, walk(child, depth+1)...)
		}
		return append(out, pid)
	}
	return walk(root, 0)
}

func TestIndexedDescendantsMatchesReference(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		table := ProcessTable{}
		for pid := 2; pid < 200; pid++ {
			table[pid] = Process{PID: pid, PPID: rng.Intn(220)}
		}
		// A separate chain must still stop at the original depth boundary.
		for pid := 300; pid < 400; pid++ {
			table[pid] = Process{PID: pid, PPID: pid - 1}
		}
		for root := -1; root < 405; root++ {
			got, want := table.Descendants(root), referenceDescendants(table, root)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed=%d root=%d: got %v, want %v", seed, root, got, want)
			}
		}
	}
}

func BenchmarkDescendantsWide(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("processes=%d", n), func(b *testing.B) {
			table := make(ProcessTable, n)
			table[2] = Process{PID: 2, PPID: 1}
			for pid := 3; pid < n+2; pid++ {
				table[pid] = Process{PID: pid, PPID: 2}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got := table.Descendants(2)
				if len(got) != n || got[n-1] != 2 {
					b.Fatal("lost tree members or root order")
				}
			}
		})
	}
}
