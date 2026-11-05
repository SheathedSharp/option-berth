package state

// Apply folds a delta into a snapshot. It is Diff's inverse, and the reason a
// stream client can follow changes without ever requesting a fresh snapshot.
func Apply(s Snapshot, d Delta) Snapshot {
	s.Seq, s.At = d.Seq, d.At
	s.Ports = applyChange(s.Ports, d.Ports, Port.Key)
	s.Groups = applyChange(s.Groups, d.Groups, Group.Key)
	s.Sessions = applyChange(s.Sessions, d.Sessions, SessionRecord.Key)
	return s
}

// applyChange rebuilds one collection from the previous rows and one Change.
// Surviving rows keep their order and new rows are appended, so a collection
// the daemon publishes sorted stays sorted except at its tail.
//
// A key present in both Removed and Added is a restart — Diff emits that pair
// when a port's PID changed — and the new row replaces the old one in place
// rather than being dropped and appended.
func applyChange[T any](prev []T, change Change[T], key func(T) string) []T {
	gone := keySet(change.Removed)
	incoming := make(map[string]T, len(change.Updated)+len(change.Added))
	for _, row := range change.Updated {
		incoming[key(row)] = row
	}
	// An add wins over an update for the same key, as the wire intends: the
	// row is new, not merely refreshed.
	for _, row := range change.Added {
		incoming[key(row)] = row
	}

	merged := make([]T, 0, len(prev)+len(change.Added))
	placed := make(map[string]bool, len(prev))
	for _, row := range prev {
		id := key(row)
		if replacement, refreshed := incoming[id]; refreshed {
			merged = append(merged, replacement)
			placed[id] = true
			continue
		}
		if !gone[id] {
			merged = append(merged, row)
			placed[id] = true
		}
	}
	for _, row := range change.Added {
		id := key(row)
		if placed[id] {
			continue
		}
		merged = append(merged, row)
		placed[id] = true
	}
	if merged == nil {
		return []T{}
	}
	return merged
}

func keySet(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}
