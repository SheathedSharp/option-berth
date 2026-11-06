package doctor

import (
	"sort"
	"strings"
)

// Prefixes lists the selectors `--only` accepts that are not check ids: the
// part before the dot of a dotted id, so a prefix selects every check under it.
func Prefixes() []string {
	seen := map[string]bool{}
	var prefixes []string
	for _, id := range IDs() {
		prefix, _, dotted := strings.Cut(id, ".")
		if !dotted || seen[prefix] {
			continue
		}
		seen[prefix] = true
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	return prefixes
}

// UnknownSelectors returns the entries of only that name no check.
func UnknownSelectors(only []string) []string {
	ids := IDs()
	var bad []string
	for _, entry := range newSelection(only) {
		one := selection{entry}
		if !one.selectsAny(ids) {
			bad = append(bad, entry)
		}
	}
	return bad
}

// selection is the `--only` argument after splitting and trimming. An empty
// selection picks every check; an entry matches its check id exactly or, when
// dotted, every check below that prefix.
type selection []string

func newSelection(only []string) selection {
	var out selection
	for _, entry := range only {
		for _, part := range strings.Split(entry, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func (s selection) includes(id string) bool {
	if len(s) == 0 {
		return true
	}
	for _, entry := range s {
		if entry == id || strings.HasPrefix(id, entry+".") {
			return true
		}
	}
	return false
}

func (s selection) selectsAny(ids []string) bool {
	for _, id := range ids {
		if s.includes(id) {
			return true
		}
	}
	return false
}
