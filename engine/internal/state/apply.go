package state

// Apply folds a delta into a snapshot and returns the result. It is Diff's
// inverse and the reason a client can follow a stream without ever asking for
// a new snapshot.
func Apply(s Snapshot, d Delta) Snapshot {
	s.Seq, s.At = d.Seq, d.At
	s.Ports = applyChange(s.Ports, d.Ports, Port.Key)
	s.Groups = applyChange(s.Groups, d.Groups, Group.Key)
	s.Sessions = applyChange(s.Sessions, d.Sessions, SessionRecord.Key)
	return s
}

// applyChange rebuilds one collection from its previous rows and one Change.
// The order of the surviving rows is preserved and genuinely new rows are
// appended, so a collection the daemon publishes sorted stays sorted apart
// from its tail.
//
// A key that appears in both Removed and Added is a restart: Diff emits the
// pair for a port whose PID changed, and the new row replaces the old one
// where it stood rather than being dropped and re-appended.
func applyChange[T any](prev []T, ch Change[T], key func(T) string) []T {
	removed := make(map[string]bool, len(ch.Removed))
	for _, k := range ch.Removed {
		removed[k] = true
	}
	replace := make(map[string]T, len(ch.Updated)+len(ch.Added))
	for _, v := range ch.Updated {
		replace[key(v)] = v
	}
	for _, v := range ch.Added {
		replace[key(v)] = v
	}

	out := make([]T, 0, len(prev)+len(ch.Added))
	kept := make(map[string]bool, len(prev))
	for _, v := range prev {
		k := key(v)
		if repl, ok := replace[k]; ok {
			out = append(out, repl)
			kept[k] = true
			continue
		}
		if removed[k] {
			continue
		}
		out = append(out, v)
		kept[k] = true
	}
	for _, v := range ch.Added {
		if k := key(v); !kept[k] {
			out = append(out, v)
			kept[k] = true
		}
	}
	if out == nil {
		out = []T{}
	}
	return out
}
