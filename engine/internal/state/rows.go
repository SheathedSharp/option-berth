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

// Tag stamps rows with the local host name used in their keys and wire data.
func (r Rows) Tag(host string) Rows {
	out := Rows{
		Ports: make([]Port, len(r.Ports)), Groups: make([]Group, len(r.Groups)),
		Sessions: make([]SessionRecord, len(r.Sessions)),
	}
	copy(out.Ports, r.Ports)
	copy(out.Groups, r.Groups)
	copy(out.Sessions, r.Sessions)
	for i := range out.Ports {
		out.Ports[i].Host = host
	}
	for i := range out.Groups {
		out.Groups[i].Host = host
	}
	for i := range out.Sessions {
		out.Sessions[i].Host = host
	}
	return out
}

// Append concatenates other onto r.
func (r Rows) Append(other Rows) Rows {
	r.Ports = append(r.Ports, other.Ports...)
	r.Groups = append(r.Groups, other.Groups...)
	r.Sessions = append(r.Sessions, other.Sessions...)
	return r
}

// Normalize replaces nil collections with empty arrays for the wire.
func (r Rows) Normalize() Rows {
	if r.Ports == nil {
		r.Ports = []Port{}
	}
	if r.Groups == nil {
		r.Groups = []Group{}
	}
	if r.Sessions == nil {
		r.Sessions = []SessionRecord{}
	}
	return r
}

// Into copies the collections onto a snapshot.
func (r Rows) Into(s Snapshot) Snapshot {
	r = r.Normalize()
	s.Ports, s.Groups, s.Sessions = r.Ports, r.Groups, r.Sessions
	return s
}
