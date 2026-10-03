// Independent pre-optimization implementation, retained only as a test oracle.
// Do not call production equality/index helpers here.
package state

import "reflect"

// oracleDiff computes the delta from prev to next, ignoring Stats when deciding
// whether a port changed — a busy process must not produce a delta on every
// scan. A port whose PID changed is reported as a remove plus an add (a
// restart), never an update, so clients can tell "same server" from "new
// process on the same socket".
func oracleDiff(prev, next Snapshot) Delta { return oracleDiffInternal(prev, next, false) }

// oracleDiffWithStats is oracleDiff except that a stats-only change counts as an update.
// Used for subscribers that opted into stats.
func oracleDiffWithStats(prev, next Snapshot) Delta { return oracleDiffInternal(prev, next, true) }

func oracleDiffInternal(prev, next Snapshot, withStats bool) Delta {
	return Delta{
		Seq:   next.Seq,
		At:    next.At,
		Ports: oracleDiffPorts(prev.Ports, next.Ports, withStats),
		Groups: oracleDiffKeyed(prev.Groups, next.Groups,
			func(g Group) string { return g.Key() },
			func(a, b Group) bool {
				return a.Status == b.Status &&
					a.Repo == b.Repo && a.Worktree == b.Worktree && a.Branch == b.Branch &&
					a.Source == b.Source &&
					reflect.DeepEqual(a.RootDir, b.RootDir) &&
					reflect.DeepEqual(a.ConfigPath, b.ConfigPath) &&
					reflect.DeepEqual(a.Members, b.Members) &&
					oracleServicesEqual(a.Services, b.Services) &&
					reflect.DeepEqual(a.Machine, b.Machine)
			}),
		Sessions: oracleDiffKeyed(prev.Sessions, next.Sessions,
			func(s SessionRecord) string { return s.Key() },
			func(a, b SessionRecord) bool { return reflect.DeepEqual(a, b) }),
	}
}

func oracleServicesEqual(a, b []Service) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		left, right := a[i], b[i]
		if !oracleHealthEqual(left.HealthStatus, right.HealthStatus) {
			return false
		}
		left.HealthStatus = nil
		right.HealthStatus = nil
		if !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}

// oracleDiffPorts keys ports by Key() and treats a PID change as remove + add.
func oracleDiffPorts(prev, next []Port, withStats bool) Change[Port] {
	// Index immutable input rows; do not copy each large row into the map.
	before := make(map[oraclePortKey]*Port, len(prev))
	for i := range prev {
		before[oraclePortIdentity(prev[i])] = &prev[i]
	}

	ch := oracleEmptyChange[Port]()
	seen := make(map[oraclePortKey]struct{}, len(next))
	for _, p := range next {
		identity := oraclePortIdentity(p)
		seen[identity] = struct{}{}
		old, existed := before[identity]
		switch {
		case !existed:
			ch.Added = append(ch.Added, p)
		case old.PID != p.PID:
			// Restart: the socket is the same but a different process owns it.
			ch.Removed = append(ch.Removed, p.Key())
			ch.Added = append(ch.Added, p)
		case !oraclePortsEqual(*old, p, withStats):
			ch.Updated = append(ch.Updated, p)
		}
	}
	for _, p := range prev {
		if _, ok := seen[oraclePortIdentity(p)]; !ok {
			ch.Removed = append(ch.Removed, p.Key())
		}
	}
	return ch
}

// oraclePortsEqual compares two ports, optionally ignoring Stats. Health latency is
// always ignored: a probe that answers in 3 ms and then in 4 ms has not
// changed, and with configured health probed on every tick a live latency in
// the comparison would publish a delta every two seconds forever. The newest
// latency still rides out with the next real change.
func oraclePortsEqual(a, b Port, withStats bool) bool {
	if !withStats {
		a.Stats = nil
		b.Stats = nil
	}
	if !oracleHealthEqual(a.Health, b.Health) {
		return false
	}
	a.Health = nil
	b.Health = nil
	return reflect.DeepEqual(a, b)
}

// oraclePortKey avoids formatting every port identity while building a delta map.
// The public string form is produced only for removed rows.
type oraclePortKey struct {
	host string
	port int
	bind string
}

func oraclePortIdentity(p Port) oraclePortKey {
	return oraclePortKey{host: p.Host, port: p.Port, bind: p.BindAddress}
}

func oracleHealthEqual(a, b *Health) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Status == b.Status && a.Code == b.Code && a.Reason == b.Reason && a.Configured == b.Configured
}

// oracleDiffKeyed is the generic collection oracleDiff used by every non-port collection.
func oracleDiffKeyed[T any](prev, next []T, key func(T) string, equal func(a, b T) bool) Change[T] {
	before := make(map[string]*T, len(prev))
	for i := range prev {
		before[key(prev[i])] = &prev[i]
	}

	ch := oracleEmptyChange[T]()
	seen := make(map[string]bool, len(next))
	for _, v := range next {
		k := key(v)
		seen[k] = true
		old, existed := before[k]
		switch {
		case !existed:
			ch.Added = append(ch.Added, v)
		case !equal(*old, v):
			ch.Updated = append(ch.Updated, v)
		}
	}
	for _, v := range prev {
		if !seen[key(v)] {
			ch.Removed = append(ch.Removed, key(v))
		}
	}
	return ch
}

// oracleEmptyChange returns a Change whose slices marshal as [] rather than null.
func oracleEmptyChange[T any]() Change[T] {
	return Change[T]{Added: []T{}, Updated: []T{}, Removed: []string{}}
}
