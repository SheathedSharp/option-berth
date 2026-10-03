package display

import "github.com/sheathedsharp/option-berth/internal/ports"

// Scope is what a `--json` document is about — the first thing a caller has to
// know, because the same rows mean something different for one project than
// for another.
//
// Kind is "worktree" (the only kind the project commands print). Name and Root
// identify the worktree: Root is left out when the caller named it itself
// and there is no directory to point at.
type Scope struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	Root string `json:"root,omitempty"`
}

// FilterPorts returns only ports matching the given type filter (docker or
// user). An empty filter keeps everything.
func FilterPorts(pp []ports.ListeningPort, filter string) []ports.ListeningPort {
	if filter == "" {
		return pp
	}
	var result []ports.ListeningPort
	for _, p := range pp {
		if p.Type.String() == filter {
			result = append(result, p)
		}
	}
	return result
}
