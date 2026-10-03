package groups

import (
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"github.com/sheathedsharp/option-berth/internal/state"
)

// resolutionPass is one synchronous observation, not an Index cache. Only
// filesystem facts and version-checked claim lookup indexes are memoized,
// never aliases, run hits or final ownership. Those are evaluated against the
// current index/registry for each row. The caller owns the index as before;
// there is no worker or new lock.
//
// Public one-off helpers use a pass with nil maps (no memoization). Batch
// callers allocate maps once, bounded by distinct inputs and walked ancestors,
// and drop them on return. This avoids cross-scan invalidation and retention.
type resolutionPass struct {
	index     *Index
	paths     map[string]string
	checkouts map[string]checkoutResult
	probed    map[string]struct{}
	heads     map[string]string
	claims    map[int]claimDirectoryIndex
}

type checkoutResult struct {
	checkout Checkout
	inside   bool
}

func newResolutionPass(index *Index) *resolutionPass {
	return &resolutionPass{index: index, paths: make(map[string]string),
		checkouts: make(map[string]checkoutResult), probed: make(map[string]struct{}),
		heads: make(map[string]string), claims: make(map[int]claimDirectoryIndex)}
}

func (r *resolutionPass) canonical(path string) string {
	if path == "" {
		return ""
	}
	if result, ok := r.paths[path]; ok {
		return result
	}
	result := Canonical(path)
	if r.paths != nil {
		r.paths[path] = result
		r.paths[result] = result
	}
	return result
}

func (r *resolutionPass) locate(dir string) (Checkout, bool) {
	if dir == "" {
		return Checkout{}, false
	}
	path := r.canonical(dir)
	if hit, ok := r.checkouts[path]; ok {
		return hit.checkout, hit.inside
	}
	co, inside := locateCanonical(path)
	if r.checkouts != nil {
		r.checkouts[path] = checkoutResult{co, inside}
	}
	return co, inside
}

func (r *resolutionPass) projectCheckout(p *state.Port) (Checkout, bool) {
	if co, ok := r.locate(p.Cwd); ok {
		return co, true
	}
	if p.Docker != nil {
		return r.locate(r.index.ComposeDir(p.Docker.ComposeProject))
	}
	return Checkout{}, false
}

func (r *resolutionPass) under(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	return underCanonical(r.canonical(path), r.canonical(dir))
}

func (r *resolutionPass) within(cfg *Config, co Checkout) bool {
	return !co.Linked() || r.under(cfg.Dir, co.Root)
}

// nearest intentionally does not memoize the config result: discovering a
// main checkout's manifest can change this relation during the same pass.
func (r *resolutionPass) nearest(dir string) *Config {
	if dir == "" {
		return nil
	}
	cur := r.canonical(dir)
	for i := 0; i < maxWalk; i++ {
		if cfg := r.index.configs[cur]; cfg != nil {
			return cfg
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return nil
}

func (r *resolutionPass) probe(dir string) {
	if r.probed != nil {
		if _, seen := r.probed[dir]; seen {
			return
		}
		r.probed[dir] = struct{}{}
	}
	r.index.probeDir(dir)
}

func (r *resolutionPass) observe(dir string) {
	if dir == "" {
		return
	}
	cur := r.canonical(dir)
	co, found := r.locate(cur)
	if found && co.Linked() {
		r.probe(co.Main)
	}
	for i := 0; i < maxWalk; i++ {
		r.probe(cur)
		if !found || cur == co.Root {
			return
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return
		}
		cur = parent
	}
}

func (r *resolutionPass) branch(co Checkout) string {
	if co.head == "" {
		return ""
	}
	if branch, ok := r.heads[co.head]; ok {
		return branch
	}
	branch := r.index.Branch(co)
	if r.heads != nil {
		r.heads[co.head] = branch
	}
	return branch
}

// High-collision ports (many projects declaring 8080, for example) must not
// require one directory containment check per project per listener. Index
// canonical directories once per queried port, then walk the listener's
// ancestors. Preserve the OLD sorted-candidate priority, including unusual
// synthetic Config.Dir aliases, rather than assuming canonical depth matches
// the original raw-path ordering.
type rankedClaim struct {
	claim portClaim
	rank  int
}
type claimDirectoryIndex struct {
	version uint64
	byDir   map[string]rankedClaim
}

func (r *resolutionPass) matchClaims(port int, cwd string, candidates []portClaim) (portClaim, bool) {
	if len(candidates) < 2 || r.claims == nil {
		for _, candidate := range candidates {
			if r.under(cwd, candidate.cfg.Dir) {
				return candidate, true
			}
		}
		return portClaim{}, false
	}
	entry, ok := r.claims[port]
	if !ok || entry.version != r.index.claimVersion {
		entry = claimDirectoryIndex{version: r.index.claimVersion, byDir: make(map[string]rankedClaim, len(candidates))}
		for rank, candidate := range candidates {
			key := directoryKey(r.canonical(candidate.cfg.Dir))
			if _, seen := entry.byDir[key]; !seen {
				entry.byDir[key] = rankedClaim{candidate, rank}
			}
		}
		r.claims[port] = entry
	}
	best := rankedClaim{rank: len(candidates)}
	for dir := r.canonical(cwd); dir != ""; {
		if hit, ok := entry.byDir[directoryKey(dir)]; ok && hit.rank < best.rank {
			best = hit
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return best.claim, best.rank < len(candidates)
}

// filepath.Rel compares Windows components with strings.EqualFold, not full
// Unicode case folding. SimpleFold's minimum representative supplies precisely
// that equivalence for map keys; ToLower alone misses e.g. long-s. Other OSes
// keep byte-exact keys, matching their filepath.Rel behavior. This is only a
// lookup key, never a path sent to the OS or published on the wire.
func directoryKey(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	return strings.Map(func(r rune) rune {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < least {
				least = f
			}
		}
		return least
	}, path)
}
