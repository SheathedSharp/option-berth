package state

import "cmp"

// Diff computes the delta from prev to next, ignoring Stats when deciding
// whether a port changed — a busy process must not produce a delta on every
// scan. A port whose PID changed is reported as a remove plus an add (a
// restart), never an update, so clients can tell "same server" from "new
// process on the same socket".
func Diff(prev, next Snapshot) Delta { return diff(prev, next, false) }

// DiffWithStats is Diff except that a stats-only change counts as an update.
// Used for subscribers that opted into stats.
func DiffWithStats(prev, next Snapshot) Delta { return diff(prev, next, true) }

func diff(prev, next Snapshot, withStats bool) Delta {
	return Delta{
		Seq:   next.Seq,
		At:    next.At,
		Ports: diffPorts(prev.Ports, next.Ports, withStats),
		Groups: diffKeyed(prev.Groups, next.Groups,
			func(g Group) string { return g.Key() }, groupsEqual),
		Sessions: diffKeyed(prev.Sessions, next.Sessions,
			func(s SessionRecord) string { return s.Key() },
			func(a, b SessionRecord) bool { return a == b }),
	}
}

// diffPorts keys ports by Key() and treats a PID change as remove + add.
func diffPorts(prev, next []Port, withStats bool) Change[Port] {
	// Exploit ordering only when present; OS collectors do not guarantee it.
	// Verify rather than assume: callers can also supply duplicate identities.
	// Ordered unique inputs need no transient hash index or retained cache.
	if portsOrdered(prev) && portsOrdered(next) {
		return diffOrderedPorts(prev, next, withStats)
	}
	return diffUnorderedPorts(prev, next, withStats)
}

func diffUnorderedPorts(prev, next []Port, withStats bool) Change[Port] {
	// Index immutable input rows; do not copy each large row into the map.
	before := make(map[portKey]*Port, len(prev))
	for i := range prev {
		before[portIdentity(prev[i])] = &prev[i]
	}

	ch := emptyChange[Port]()
	seen := make(map[portKey]struct{}, len(next))
	for _, p := range next {
		identity := portIdentity(p)
		seen[identity] = struct{}{}
		old, existed := before[identity]
		switch {
		case !existed:
			ch.Added = append(ch.Added, p)
		case old.PID != p.PID:
			// Restart: the socket is the same but a different process owns it.
			ch.Removed = append(ch.Removed, p.Key())
			ch.Added = append(ch.Added, p)
		case !portsEqual(old, &p, withStats):
			ch.Updated = append(ch.Updated, p)
		}
	}
	for _, p := range prev {
		if _, ok := seen[portIdentity(p)]; !ok {
			ch.Removed = append(ch.Removed, p.Key())
		}
	}
	return ch
}

// portKey avoids formatting every port identity while building a delta map.
// The public string form is produced only for removed rows.
type portKey struct {
	host string
	port int
	bind string
}

func portIdentity(p Port) portKey { return portKey{host: p.Host, port: p.Port, bind: p.BindAddress} }

func healthEqual(a, b *Health) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Status == b.Status && a.Code == b.Code && a.Reason == b.Reason && a.Configured == b.Configured
}

// diffKeyed is the generic collection diff used by every non-port collection.
func diffKeyed[T any](prev, next []T, key func(T) string, equal func(a, b T) bool) Change[T] {
	before := make(map[string]*T, len(prev))
	for i := range prev {
		before[key(prev[i])] = &prev[i]
	}

	ch := emptyChange[T]()
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

// emptyChange returns a Change whose slices marshal as [] rather than null.
func emptyChange[T any]() Change[T] {
	return Change[T]{Added: []T{}, Updated: []T{}, Removed: []string{}}
}

// comparePortIdentity orders exactly the identity used by the hash fallback.
// It does not format public keys or normalize host aliases.
func comparePortIdentity(a, b *Port) int {
	if c := cmp.Compare(a.Host, b.Host); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Port, b.Port); c != 0 {
		return c
	}
	return cmp.Compare(a.BindAddress, b.BindAddress)
}

func portsOrdered(pp []Port) bool {
	for i := 1; i < len(pp); i++ {
		if comparePortIdentity(&pp[i-1], &pp[i]) >= 0 {
			return false
		}
	}
	return true
}

// diffOrderedPorts is a streaming merge join. The second walk emits vanished
// keys AFTER restart removals, preserving the old delta's exact ordering.
// Work is O(len(prev)+len(next)), with O(1) scratch space plus the output.
func diffOrderedPorts(prev, next []Port, withStats bool) Change[Port] {
	ch := emptyChange[Port]()
	i := 0
	for j := range next {
		p := &next[j]
		for i < len(prev) && comparePortIdentity(&prev[i], p) < 0 {
			i++
		}
		switch {
		case i == len(prev) || comparePortIdentity(&prev[i], p) != 0:
			ch.Added = append(ch.Added, *p)
		case prev[i].PID != p.PID:
			ch.Removed = append(ch.Removed, p.Key())
			ch.Added = append(ch.Added, *p)
		case !portsEqual(&prev[i], p, withStats):
			ch.Updated = append(ch.Updated, *p)
		}
	}
	i = 0
	for j := range prev {
		p := &prev[j]
		for i < len(next) && comparePortIdentity(&next[i], p) < 0 {
			i++
		}
		if i == len(next) || comparePortIdentity(&next[i], p) != 0 {
			ch.Removed = append(ch.Removed, p.Key())
		}
	}
	return ch
}
