// Package git reports a repository's current state for callers that need to
// show it: which branch a checkout is on, which worktrees exist, what has
// changed, where it stands against its upstream — and, past the overview, which
// files those changes are (ReadTree) and what they say line by line (Diff).
//
// The three reads are the three stages of docs/product.md's 「代码的实况」:
// overview, tree, diff.
//
// It is **read-only by construction** — no subcommand that writes is ever built
// here. See docs/product.md ("代码的实况"): writing code has better authors
// (agents, editors), so this side only reports.
//
// The work is done by the `git` binary rather than by parsing `.git/index`.
// `--porcelain=v2` is the interface git promises to keep stable, while the
// index is a cache layer (v2/v3/v4, split index, untracked cache) that a second
// implementation would eventually disagree with. Worktree membership also
// comes from git's own porcelain interface, not a second repository model.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// ErrNotARepository is what Read says about a directory that sits in no
// checkout.
var ErrNotARepository = errors.New("not a git repository")

// ErrGitUnavailable is what Read says when the git binary is missing. The CLI
// turns it into a hint, because "exec: git: executable file not found" is not
// one.
var ErrGitUnavailable = errors.New("git is not on PATH")

// timeout is the shared budget for an overview, not a fresh budget per command.
const timeout = 5 * time.Second

// Snapshot is a repository's state as of one read. Every field is derived; none
// of it is cached, because the answer changes every time an agent saves a file.
type Snapshot struct {
	Root string `json:"root"`
	// Branch is the checked-out branch, empty for a detached HEAD (Head then
	// carries the commit).
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Head     string `json:"head,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`

	// Staged and Unstaged count files, not hunks: a file with both staged and
	// unstaged changes counts in each.
	Staged    int `json:"staged"`
	Unstaged  int `json:"unstaged"`
	Untracked int `json:"untracked"`
	Conflicts int `json:"conflicts"`

	Last      *Commit    `json:"last_commit,omitempty"`
	Worktrees []Worktree `json:"worktrees"`
}

// Clean reports whether the checkout has nothing to show a person: no staged,
// unstaged, untracked or conflicted paths.
func (s Snapshot) Clean() bool {
	return s.Staged == 0 && s.Unstaged == 0 && s.Untracked == 0 && s.Conflicts == 0
}

// Commit is the last commit of a branch: enough to say where the checkout is
// without being a log viewer.
type Commit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	When    string `json:"when,omitempty"`
}

// Worktree is one checkout of the repository. Parallel agents work in linked
// worktrees, so this list is how a person tells "which berth" they are looking
// at (docs/product.md, "worktree 是并列的泊位").
type Worktree struct {
	Path   string `json:"path"`
	Head   string `json:"head,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Detached and Bare are git's own distinctions, passed through.
	Detached bool `json:"detached,omitempty"`
	Bare     bool `json:"bare,omitempty"`
	// Current marks the worktree that holds the directory Read was given.
	Current bool `json:"current,omitempty"`
}

// Read reports the state of the repository containing dir.
func Read(ctx context.Context, dir string) (Snapshot, error) {
	root, _, ok := groups.Find(dir)
	if !ok {
		return Snapshot{}, ErrNotARepository
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	snap, _, err := readStatus(ctx, dir)
	if err != nil {
		return Snapshot{}, err
	}
	snap.Root = root
	if err := readRest(ctx, dir, &snap); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// readStatus is the one call that answers most of what a snapshot is: the
// branch, HEAD, ahead/behind, and one record per changed path.
//
// -uall is deliberate. Without it git folds a whole untracked directory into a
// single "?? dist/" record, and the counts above a list of files would then
// disagree with the list: the same five hundred files are one thing to the
// count and five hundred rows to the reader.
func readStatus(ctx context.Context, dir string) (Snapshot, []File, error) {
	out, err := gitOut(ctx, dir, "status", "--porcelain=v2", "--branch", "-z", "-uall")
	if err != nil {
		return Snapshot{}, nil, err
	}
	var snap Snapshot
	files := parseStatus(out, &snap)
	return snap, files, nil
}

// gitRead is a per-call seam: tests can control completion order without
// replacing global state or installing a fake executable on the user's PATH.
type gitRead func(context.Context, string, ...string) ([]byte, error)

// readRest fills in what status does not carry. The two independent metadata
// commands overlap, with at most two subprocesses and one joined goroutine
// per read. Neither a background worker nor a cross-request cache is retained.
func readRest(ctx context.Context, dir string, snap *Snapshot) error {
	return readRestWith(ctx, dir, snap, gitOut)
}

func readRestWith(ctx context.Context, dir string, snap *Snapshot, read gitRead) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		out []byte
		err error
	}
	worktrees := make(chan result, 1)
	go func() {
		out, err := read(ctx, dir, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			cancel() // a required read failed; do not wait out the optional one
		}
		worktrees <- result{out: out, err: err}
	}()

	var last *Commit
	if snap.Head != "" {
		// Pin the commit observed by status, not whichever HEAD a concurrent
		// checkout/commit leaves behind. An unborn branch needs no log process.
		if out, err := read(ctx, dir, "log", "-1", "--format=%h%x00%s%x00%cI", snap.Head, "--"); err == nil {
			last = parseCommit(out)
		}
	}
	// Always join, including failure/cancellation paths. CommandContext plus
	// gitRun's WaitDelay bound both process and inherited-pipe cleanup.
	wt := <-worktrees
	if wt.err != nil {
		return wt.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	list := parseWorktrees(wt.out)
	for i := range list {
		if groups.Canonical(list[i].Path) == snap.Root {
			list[i].Current = true
		}
	}
	// Publish only after both commands finish. Other worktrees are separate
	// observations; this does not claim a transaction across the repository.
	snap.Worktrees, snap.Last = list, last
	return nil
}

// gitOut runs one read-only git command in dir.
func gitOut(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, stderr, err := gitRun(ctx, dir, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrGitUnavailable
		}
		// git puts the reason on stderr; "exit status 128" alone is unhelpful.
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, err
	}
	return out, nil
}

// gitOutDiff is gitOut for `git diff --no-index`, which exits 1 to say the two
// sides differ — the plain answer when the left side is /dev/null and the right
// side is an untracked file. The patch is on stdout either way.
func gitOutDiff(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, stderr, err := gitRun(ctx, dir, args...)
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return out, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, ErrGitUnavailable
	}
	if msg := strings.TrimSpace(string(stderr)); msg != "" {
		return nil, fmt.Errorf("%s", msg)
	}
	return nil, err
}

// gitRun keeps stdout/stderr separate and suppresses optional index refresh
// writes. It never changes repository configuration or stages user files.
func gitRun(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	// Optional locks alone do not stop a configured fsmonitor hook/daemon.
	// UI reads must not start repository monitoring or background maintenance.
	// These overrides belong to this invocation (including Git's children), not
	// .git/config. Keep ignore/attribute/index semantics and existing filters.
	prefix := []string{"--no-pager", "--no-optional-locks",
		"-c", "core.fsmonitor=false", "-c", "maintenance.auto=false",
		"-c", "gc.auto=0", "-c", "log.showSignature=false", "-C", dir}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	cmd.WaitDelay = 100 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// Root returns the checkout that contains dir, or "" when it is in none. It is
// for callers that need the path without paying for a status read.
func Root(dir string) string {
	root, _, ok := groups.Find(dir)
	if !ok {
		return ""
	}
	return root
}
