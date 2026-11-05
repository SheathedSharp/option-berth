package state

// Rows is one scan's contribution to the published collections.
type Rows struct {
	Ports    []Port
	Groups   []Group
	Sessions []SessionRecord
}

// RowsOf takes the collections out of a snapshot.
func RowsOf(s Snapshot) Rows {
	return Rows{Ports: s.Ports, Groups: s.Groups, Sessions: s.Sessions}
}

// Tag stamps every row with the host name its keys and wire data use. The
// input rows are copied, never mutated.
func (r Rows) Tag(host string) Rows {
	return Rows{
		Ports:    tagEach(r.Ports, func(row *Port) { row.Host = host }),
		Groups:   tagEach(r.Groups, func(row *Group) { row.Host = host }),
		Sessions: tagEach(r.Sessions, func(row *SessionRecord) { row.Host = host }),
	}
}

func tagEach[T any](rows []T, apply func(*T)) []T {
	out := make([]T, len(rows))
	copy(out, rows)
	for i := range out {
		apply(&out[i])
	}
	return out
}

// Append concatenates other onto this set of rows.
func (r Rows) Append(other Rows) Rows {
	r.Ports = append(r.Ports, other.Ports...)
	r.Groups = append(r.Groups, other.Groups...)
	r.Sessions = append(r.Sessions, other.Sessions...)
	return r
}

// Normalize replaces nil collections with empty arrays for the wire.
func (r Rows) Normalize() Rows {
	return Rows{
		Ports:    nonNil(r.Ports),
		Groups:   nonNil(r.Groups),
		Sessions: nonNil(r.Sessions),
	}
}

func nonNil[T any](rows []T) []T {
	if rows == nil {
		return []T{}
	}
	return rows
}

// Into copies the collections onto a snapshot.
func (r Rows) Into(s Snapshot) Snapshot {
	normalized := r.Normalize()
	s.Ports, s.Groups, s.Sessions = normalized.Ports, normalized.Groups, normalized.Sessions
	return s
}
