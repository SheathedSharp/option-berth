// Package claims keeps port reservations for a project and worktree pair.
//
// A claim is book-keeping, not a socket: option-berth records that a port is
// spoken for so agents in parallel worktrees stop choosing the same one, and
// so the same checkout receives the same ports on every start. Nothing stops
// an outside process from binding a claimed port, which is why every acquire
// checks the live listener table before handing a port back.
//
// A key is `<project>/<worktree>`. Its starting port is a stable hash of the
// key into a block of BlockSize ports inside the configured range, and a probe
// walks upward from there.
package claims

import (
	"fmt"
	"strings"
)

const (
	// DefaultWorktree names the primary checkout, so a repository nobody has
	// branched from still produces a complete key.
	DefaultWorktree = "main"
	// BlockSize is the stride between derived starting ports. Neighbouring
	// keys land in different blocks, so a project taking a handful of ports
	// does not step into the next key's block.
	BlockSize = 10
)

// Range is the window claims are derived from. The default sits above the
// usual dev-server ports and below every OS ephemeral range (Linux 32768+,
// macOS 49152+), so a claimed port is never one the kernel hands to an
// outgoing connection.
type Range struct {
	Start int
	End   int
}

// DefaultRange is the range used when a caller configures none.
var DefaultRange = Range{Start: 10000, End: 32699}

// Valid reports whether r is a usable port window.
func (r Range) Valid() error {
	switch {
	case r.Start < 1, r.End > 65535, r.Start > r.End:
		return fmt.Errorf("claims: invalid port range %d-%d", r.Start, r.End)
	}
	return nil
}

// Span is how many ports the window holds.
func (r Range) Span() int { return r.End - r.Start + 1 }

// Key builds the claim key for a project and worktree. An empty worktree is
// the primary checkout, which the key names "main".
func Key(project, worktree string) string {
	project = trimSlashes(project)
	worktree = trimSlashes(worktree)
	if worktree == "" {
		worktree = DefaultWorktree
	}
	return project + "/" + worktree
}

func trimSlashes(value string) string {
	return strings.Trim(strings.TrimSpace(value), "/")
}

// ServiceKey is the key for one `port: auto` service in one checkout: the
// checkout key plus the service name. The same service therefore gets the same
// port every time this checkout starts it, and a different one in another
// checkout of the project.
func ServiceKey(project, worktree, service string) string {
	checkoutKey := Key(project, worktree)
	return checkoutKey + "/" + strings.TrimSpace(service)
}

// SplitKey recovers the project and worktree a key was built from. A key with
// no separator is all project, with the worktree defaulted.
func SplitKey(key string) (project, worktree string) {
	project, worktree, separated := strings.Cut(strings.TrimSpace(key), "/")
	if !separated || strings.TrimSpace(worktree) == "" {
		return project, DefaultWorktree
	}
	return project, worktree
}

// Base is the port a key's probe starts from. The hash runs over the key
// alone, so every option-berth process on the machine derives the same port.
func (r Range) Base(key string) int {
	slot := fnv1a32(key)
	blocks := r.Span() / BlockSize
	if blocks < 1 {
		return r.Start + int(slot%uint32(r.Span()))
	}
	return r.Start + int(slot%uint32(blocks))*BlockSize
}

// At is the i-th candidate port for key: Base plus i, wrapping to the start of
// the window rather than running off its end.
func (r Range) At(key string, i int) int {
	span := r.Span()
	fromStart := r.Base(key) - r.Start
	wrapped := ((fromStart+i)%span + span) % span
	return r.Start + wrapped
}

// fnv1a32 is the 32-bit FNV-1a hash of text. It is written out rather than
// imported so the constants stay visible: the derived port is a contract with
// every other option-berth on the machine and must not drift with a library
// change.
func fnv1a32(text string) uint32 {
	const (
		offsetBasis uint32 = 2166136261
		prime       uint32 = 16777619
	)
	hash := offsetBasis
	for index := 0; index < len(text); index++ {
		hash ^= uint32(text[index])
		hash *= prime
	}
	return hash
}
