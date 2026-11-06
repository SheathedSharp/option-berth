package claims

import (
	"fmt"
	"hash/fnv"
)

// DefaultWorktree names the main checkout, so an address always has a worktree
// half even when nobody branched.
const DefaultWorktree = "main"

// The product's claim window: 10-port blocks in 10000–32699, above the usual
// dev-server ports and below every OS ephemeral range (Linux 32768+, macOS
// 49152+), so a reserved port is never one the kernel hands to an outgoing
// connection. The stride is what keeps neighbouring addresses from probing over
// each other before the table is consulted.
const (
	blockSize  = 10
	claimFirst = 10000
	claimLast  = 32699
)

// Range is the window ports are derived from.
//
// Its bounds are unexported and it is built by RangeOf, so a window that starts
// below 1, ends above 65535 or runs backwards cannot exist in a running
// program. The zero value is the product's own window.
type Range struct{ first, last int }

// DefaultRange is the window used when a caller does not configure one.
func DefaultRange() Range { return Range{first: claimFirst, last: claimLast} }

// RangeOf builds a window, refusing one no port could be derived from.
func RangeOf(first, last int) (Range, error) {
	if first < 1 || last > 65535 || first > last {
		return Range{}, fmt.Errorf("claims: invalid port range %d-%d", first, last)
	}
	return Range{first: first, last: last}, nil
}

// First is the lowest port in the window.
func (r Range) First() int { return r.bounds().first }

// Last is the highest port in the window.
func (r Range) Last() int { return r.bounds().last }

// Span is how many ports the window holds.
func (r Range) Span() int { return r.bounds().size() }

// Base is the port an address probes from first. The same address derives the
// same start in this process and in every other one, because the hash is fnv-1a
// over the key alone.
//
// A window narrower than one block has no block to stride into, so the hash is
// spread across the whole of it instead.
func (r Range) Base(key string) int {
	w := r.bounds()
	hashed := fnv1a32(key)
	blocks := uint32(w.size() / blockSize)
	if blocks == 0 {
		return w.first + int(hashed%uint32(w.size()))
	}
	return w.first + int(hashed%blocks)*blockSize
}

// Walk is the i-th port probed from base, counting up and wrapping back to the
// start of the window rather than running off its end. It takes base already
// computed, so a probe across a whole window hashes the key once.
func (r Range) Walk(base, i int) int {
	w := r.bounds()
	span := w.size()
	return w.first + (base-w.first+i%span+span)%span
}

// bounds fills the zero value with the product's window, and is the one place
// that knows what that window is.
func (r Range) bounds() Range {
	if r.first == 0 && r.last == 0 {
		return DefaultRange()
	}
	return r
}

func (r Range) size() int { return r.last - r.first + 1 }

// fnv1a32 is the 32-bit FNV-1a hash of s. The derived port is a contract with
// every other option-berth on the machine — two checkouts only agree on where a
// service lives because they both compute the same number — so the standard
// implementation is used rather than a local copy, and the golden test holds
// its output to fixed values.
func fnv1a32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}
