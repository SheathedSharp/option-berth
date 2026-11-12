package groups

import (
	"os"
	"path/filepath"
	"strings"
)

// Checkout is where a directory sits in git.
//
// One repository can be checked out several times: its main checkout and any
// number of linked worktrees (`git worktree add`). Each is a group of its own —
// `<project>` for the main checkout, `<project>@<worktree>` for a linked one —
// and all of them share the project name, which only the main checkout
// decides. A worktree's copy of a committed `oberth.yaml` describes its
// services and never its name, so a stale copy cannot fold the worktree back
// into the main checkout's group.
type Checkout struct {
	// Root is the directory holding the `.git` entry.
	Root string
	// Main is the main checkout's root: Root itself unless this is a linked
	// worktree. A submodule, and a `.git` file nothing can parse, are their own
	// main checkout.
	Main string
	// Worktree is a linked worktree's name, the base name of Root. It is empty
	// for a main checkout.
	Worktree string
	// head points at the HEAD file the branch is read from; "" when there is
	// nothing to read.
	head string
}

// Linked reports whether this checkout is a linked worktree.
func (c Checkout) Linked() bool { return c.Worktree != "" }

// Locate finds the checkout containing dir. It climbs the way Find does and
// answers the same root, plus the main checkout of a linked worktree and the
// HEAD file its branch is read from. Paths are canonical.
func Locate(dir string) (Checkout, bool) {
	return locateCanonical(Canonical(dir))
}

// locateCanonical walks an already normalized path. Callers outside this
// package must use Locate; a resolution pass normalizes each distinct input
// only once.
func locateCanonical(cur string) (Checkout, bool) {
	if cur == "" {
		return Checkout{}, false
	}
	for climbed := 0; climbed < maxWalk; climbed++ {
		gitPath := filepath.Join(cur, ".git")
		if info, statErr := os.Lstat(gitPath); statErr == nil {
			return checkoutAt(cur, gitPath, info.Mode().IsRegular()), true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return Checkout{}, false
}

// checkoutAt describes the checkout rooted at root, whose `.git` entry is a
// directory or, for a linked worktree or submodule, a file pointing at the
// admin directory.
func checkoutAt(root, gitPath string, isFile bool) Checkout {
	co := Checkout{Root: root, Main: root}
	if !isFile {
		co.head = headIn(gitPath)
		return co
	}
	admin := gitdirTarget(root, gitPath)
	if admin == "" {
		return co
	}
	co.head = headIn(admin)
	if main := worktreeMain(admin); main != "" {
		co.Main = Canonical(main)
		co.Worktree = filepath.Base(root)
	}
	return co
}

// headIn is the HEAD file inside a git directory.
func headIn(gitDir string) string { return filepath.Join(gitDir, "HEAD") }

// Branch reads this checkout's HEAD. Equal mtime and length do not prove equal
// bytes — same-length branch switches and atomic replacement are both valid —
// so the file is read. A resolution pass shares one read per HEAD, never across
// passes.
func (x *Index) Branch(co Checkout) string {
	if co.head == "" {
		return ""
	}
	data, err := os.ReadFile(co.head)
	if err != nil {
		return ""
	}
	return parseHead(data)
}

// parseHead reads the branch out of a HEAD file: `ref: refs/heads/<branch>`.
// A detached HEAD holds a commit id instead, and a ref outside refs/heads is
// not a branch; both answer "".
func parseHead(data []byte) string {
	ref, isRef := strings.CutPrefix(strings.TrimSpace(string(data)), "ref:")
	if !isRef {
		return ""
	}
	branch, isBranch := strings.CutPrefix(strings.TrimSpace(ref), "refs/heads/")
	if !isBranch {
		return ""
	}
	return branch
}

// SetAliases installs the project names `groups.rename` stores for projects
// with no `oberth.yaml` to hold one, keyed by the main checkout's root. The
// scanner refreshes them every tick; a nil map clears them.
func (x *Index) SetAliases(aliases map[string]string) {
	x.aliases = make(map[string]string, len(aliases))
	for root, name := range aliases {
		if root == "" || name == "" {
			continue
		}
		x.aliases[filepath.Clean(root)] = name
	}
}

// fileProjectName is the project name before any alias: the name the main
// checkout's `oberth.yaml` gives it, else the main checkout's directory name.
// The main checkout is probed here, because a process in a linked worktree is
// what usually brings a project to the index's attention, and its walk stops
// at the worktree's own root.
func (r *resolutionPass) fileProjectName(main string) string {
	r.probe(main)
	if cfg := r.nearest(main); cfg != nil {
		return cfg.Name
	}
	return filepath.Base(main)
}

// ProjectName is the name every checkout of one repository shares, decided by
// the main checkout at main: a stored alias, else the `name:` of the main
// checkout's `oberth.yaml`, else its directory name.
func (x *Index) ProjectName(main string) string {
	return (&resolutionPass{index: x}).projectName(main)
}

func (r *resolutionPass) projectName(main string) string {
	if alias := r.index.aliases[main]; alias != "" {
		return alias
	}
	return r.fileProjectName(main)
}

// CheckoutName is a checkout's group name: the project name, qualified as
// `<project>@<worktree>` for a linked worktree.
func (x *Index) CheckoutName(co Checkout) string {
	return (&resolutionPass{index: x}).checkoutName(co)
}

func (r *resolutionPass) checkoutName(co Checkout) string {
	name := r.projectName(co.Main)
	if !co.Linked() {
		return name
	}
	return name + "@" + co.Worktree
}

// GroupOf answers the group a config's services are published under. A config
// at a checkout's root belongs to that checkout, so it takes the checkout's
// group name instead of its `name:` — which is what stops a worktree's copy of
// a committed file from claiming the main checkout's name. Below the root, a
// config is a nested project with its own name, qualified with the worktree
// when it sits in one. Outside any checkout, the file names itself.
func (x *Index) GroupOf(cfg *Config) string {
	return (&resolutionPass{index: x}).groupOf(cfg)
}

func (r *resolutionPass) groupOf(cfg *Config) string {
	co, inRepo := r.locate(cfg.Dir)
	if !inRepo {
		return cfg.Name
	}
	switch {
	case cfg.Dir == co.Root:
		return r.checkoutName(co)
	case co.Linked():
		return cfg.Name + "@" + co.Worktree
	default:
		return cfg.Name
	}
}

// NearestFor is Nearest confined to dir's own checkout when that checkout is a
// linked worktree. Tools put worktrees inside the main checkout (for example
// `<repo>/.claude/worktrees/<name>`), and an unconfined walk from one of them
// climbs straight into the main checkout's `oberth.yaml`.
func (x *Index) NearestFor(dir string) *Config {
	co, inRepo := Locate(dir)
	if !inRepo {
		return x.Nearest(dir)
	}
	cfg := x.Nearest(dir)
	if cfg == nil || !x.within(cfg, co) {
		return nil
	}
	return cfg
}

// within reports whether cfg can describe something in co: any config for a
// main checkout, and only one inside the worktree for a linked checkout.
func (x *Index) within(cfg *Config, co Checkout) bool {
	return !co.Linked() || Under(cfg.Dir, co.Root)
}

// runGroup is the post-step that keeps an `oberth start` run in its checkout's
// group. A run records the group its client inferred when it started, and a
// client knows neither the daemon's aliases nor, if it predates that rule, that
// a worktree is not its main checkout. So a run group that is the project's own
// name — with or without the worktree — is the checkout's group under its
// current name. Any other name was chosen with --group and stays.
func (r *resolutionPass) runGroup(group string, co Checkout) string {
	base := r.fileProjectName(co.Main)
	if !co.Linked() {
		if group == base {
			return r.projectName(co.Main)
		}
		return group
	}
	current := r.checkoutName(co)
	alias := r.index.aliases[co.Main]
	switch {
	case group == base, group == base+"@"+co.Worktree:
		return current
	case alias != "" && (group == alias || group == alias+"@"+co.Worktree):
		return current
	default:
		return group
	}
}
