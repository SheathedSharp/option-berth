package ports

// MaxAncestry bounds every PPID walk in the engine against a cyclic or
// pathological process table. It is a process-tree fact, so it lives with the
// table rather than in each walker.
const MaxAncestry = 64

// Ancestor walks from pid through the process table's parent links, calling
// visit at every step, and returns the first hit. A missing parent, a pid at
// or below init, and a cycle all end the walk; the returned bool reports
// whether visit ever answered true.
//
// This is the one ancestry walk the engine uses: the daemon's run registry and
// the direct-scan registry both resolve "which run owns this pid" through it,
// so the walk's bounds and cycle rules cannot drift apart.
func Ancestor[T any](pid int, parents map[int]int, visit func(int) (T, bool)) (T, bool) {
	var zero T
	if pid <= 1 || visit == nil {
		return zero, false
	}
	cur := pid
	for i := 0; i < MaxAncestry; i++ {
		if hit, ok := visit(cur); ok {
			return hit, true
		}
		next, ok := parents[cur]
		if !ok || next <= 1 || next == cur {
			return zero, false
		}
		cur = next
	}
	return zero, false
}
