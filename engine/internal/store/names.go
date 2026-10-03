package store

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidName is returned when a name is empty or holds a separator. Same
// rule as oberth.yaml service names in the daemon spec.
var ErrInvalidName = errors.New("name must be non-empty and free of whitespace, / and \\")

func validName(name string) error {
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, " \t\n\r/\\") {
		return fmt.Errorf("%q: %w", name, ErrInvalidName)
	}
	return nil
}

// allKeyed reads a two-column key→value table whole.
func (s *Store) allKeyed(query string) (map[string]string, error) {
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
