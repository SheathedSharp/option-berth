package sessions

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// GitContext names where a command is running: the linked worktree, if there
// is one, and the branch or short commit it is on.
//
// The worktree is the checkout's directory name only for a linked worktree
// created by `git worktree add`; the primary checkout reports no worktree at
// all, so a "worktree feature-x" badge never appears where none exists.
//
// The answer is read straight from the `.git` entry. This runs before every
// dev-server start, so paying for a git subprocess here is not an option.
func GitContext(cwd string) (worktree, branch string) {
	root, linked, found := groups.Find(cwd)
	if !found {
		return "", ""
	}
	if linked != "" {
		worktree = filepath.Base(root)
	}
	return worktree, currentBranch(root)
}

// Capture is the spawn path's entry point: detect the session, then attach
// the git context of the directory the command will run in.
func Capture(cwd string, opts Options) (state.Session, bool) {
	session, found := Detect(opts)
	if !found {
		return state.Session{}, false
	}
	session.Worktree, session.Branch = GitContext(cwd)
	return session, true
}

// currentBranch reads HEAD for a checkout root. A linked worktree or a
// submodule points at its real git directory through a `.git` file instead of
// a `.git` directory, so that pointer is followed first.
func currentBranch(checkout string) string {
	gitPath := filepath.Join(checkout, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return ""
	}
	if info.Mode().IsRegular() {
		gitPath = gitDirFromFile(checkout, gitPath)
		if gitPath == "" {
			return ""
		}
	}
	return branchFromHEAD(readFileTrimmed(filepath.Join(gitPath, "HEAD")))
}

// gitDirFromFile resolves the `gitdir:` pointer in a `.git` file. A relative
// target is relative to the checkout, not to the current working directory.
func gitDirFromFile(checkout, file string) string {
	for _, raw := range strings.Split(readFileTrimmed(file), "\n") {
		line := strings.TrimSpace(raw)
		rest, isPointer := strings.CutPrefix(line, "gitdir:")
		if !isPointer {
			continue
		}
		target := strings.TrimSpace(rest)
		if target == "" {
			return ""
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(checkout, target)
		}
		return filepath.Clean(target)
	}
	return ""
}

// branchFromHEAD turns the contents of HEAD into a display name: the branch
// for a symbolic ref, the short hash for a detached HEAD. A ref outside
// refs/heads/ (a remote-tracking ref, for instance) is shown as it is spelled.
func branchFromHEAD(head string) string {
	if head == "" {
		return ""
	}
	if symbolic, isRef := strings.CutPrefix(head, "ref:"); isRef {
		ref := strings.TrimSpace(symbolic)
		if name, onBranch := strings.CutPrefix(ref, "refs/heads/"); onBranch {
			return name
		}
		return ref
	}
	if len(head) > 12 {
		return head[:12]
	}
	return head
}

func readFileTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
