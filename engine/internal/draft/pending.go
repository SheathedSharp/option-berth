package draft

import (
	"os"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/paths"
)

// Pending is a draft sitting on disk that nobody has adopted: what
// `oberth init adopt` would write into the manifest if a person said
// yes.
type Pending struct {
	Path string `json:"path"`
	// Group is the group the draft is keyed by — the name adoption resolves.
	Group string `json:"group"`
	// At, Agent and Root come from the header archive wrote. They are empty when
	// that line cannot be read: the draft is still there, and saying so beats
	// pretending it is not.
	At    string `json:"at,omitempty"`
	Agent string `json:"agent,omitempty"`
	Root  string `json:"root,omitempty"`
}

// PendingFor reads the draft waiting for one group, if there is one.
//
// Nothing but `init adopt` has ever read these files, so this is the
// first caller that has to answer "is there one" — and it is worth answering
// from the file itself rather than from a directory listing: the name is the
// group's, so a listing would have to know how groups are named anyway.
func PendingFor(group string) (Pending, bool) {
	if strings.TrimSpace(group) == "" {
		return Pending{}, false
	}
	path := paths.Draft(group)
	data, err := os.ReadFile(path)
	if err != nil {
		return Pending{}, false
	}
	p := Pending{Path: path, Group: group}
	if agent, at, root, ok := parseHeader(firstLine(string(data))); ok {
		p.Agent, p.At, p.Root = agent, at, root
	}
	return p, true
}

// parseHeader reads the provenance line archive writes:
//
//	# Drafted by <agent> (<pinned>) on <at>, from <root>.
//
// It is written by this package and read by this package, so the format has one
// home — the pinned version in parentheses is skipped rather than parsed: it is
// there for a person reading the file, and nothing here acts on it.
func parseHeader(line string) (agent, at, root string, ok bool) {
	const prefix = "# Drafted by "
	if !strings.HasPrefix(line, prefix) {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(line, prefix)

	name, tail, ok := strings.Cut(rest, " (")
	if !ok {
		return "", "", "", false
	}
	_, afterOn, ok := strings.Cut(tail, ") on ")
	if !ok {
		return "", "", "", false
	}
	at, root, ok = strings.Cut(afterOn, ", from ")
	if !ok {
		return "", "", "", false
	}
	return name, at, strings.TrimSuffix(strings.TrimSpace(root), "."), true
}
