package ports

import "testing"

func TestAncestorFindsTheNearestHit(t *testing.T) {
	parents := map[int]int{400: 300, 300: 200, 200: 100, 100: 1}
	owned := map[int]string{100: "run-a"}
	got, ok := Ancestor(400, parents, func(pid int) (string, bool) {
		name, found := owned[pid]
		return name, found
	})
	if !ok || got != "run-a" {
		t.Fatalf("Ancestor = (%q, %v), want (run-a, true)", got, ok)
	}

	// The nearest owner wins when two ancestors are registered.
	owned[200] = "run-b"
	got, ok = Ancestor(400, parents, func(pid int) (string, bool) {
		name, found := owned[pid]
		return name, found
	})
	if !ok || got != "run-b" {
		t.Fatalf("nearest ancestor = (%q, %v), want (run-b, true)", got, ok)
	}
}

func TestAncestorStopsAtMissingParentsAndInit(t *testing.T) {
	// 400's parent was never observed: visiting 400 must not be followed by a
	// guess about anything above it.
	if _, ok := Ancestor(400, map[int]int{}, func(int) (string, bool) { return "", false }); ok {
		t.Fatal("a walk with no parent links reported a hit")
	}
	// pid at or below init is never visited.
	visited := false
	Ancestor(1, map[int]int{}, func(int) (string, bool) { visited = true; return "", false })
	if visited {
		t.Fatal("pid 1 was visited")
	}
}

func TestAncestorTerminatesOnCycles(t *testing.T) {
	// Pathological: 10 <-> 11. Must terminate with no hit.
	parents := map[int]int{10: 11, 11: 10}
	if _, ok := Ancestor(10, parents, func(int) (string, bool) { return "", false }); ok {
		t.Fatal("a cyclic table produced a hit")
	}
}

func TestAncestorRespectsDepthBound(t *testing.T) {
	parents := map[int]int{}
	for i := 100; i < 100+MaxAncestry+10; i++ {
		parents[i] = i + 1
	}
	// The owner sits beyond the bound: the walk must stop, not loop forever.
	owned := map[int]string{100 + MaxAncestry + 5: "too-far"}
	got, ok := Ancestor(100, parents, func(pid int) (string, bool) {
		name, found := owned[pid]
		return name, found
	})
	if ok {
		t.Fatalf("walk passed the depth bound and returned %q", got)
	}
}
