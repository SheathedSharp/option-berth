package git

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// treeTimeout bounds a tree read, which costs more than a status: the line
// counts diff every changed file against HEAD.
const treeTimeout = 15 * time.Second

// Tree is a Snapshot plus the file rows its counts are made of: one entry per
// path that differs from HEAD, or is untracked. The overview answers "how much
// moved"; this answers "which files" (docs/product.md, 「代码的实况」的第二步).
//
// It carries the whole overview on purpose: a caller that wants the list wants
// the branch it is on too, and one read beats two.
type Tree struct {
	Snapshot
	Files []File `json:"files"`
}

// File is one path that differs from HEAD, or is untracked. Clean files are not
// here: this is a list of what needs looking at, not a file browser.
type File struct {
	Path string `json:"path"`
	// OldPath is where a rename or copy came from.
	OldPath string `json:"old_path,omitempty"`
	// Status is git's short-format pair — the index column then the worktree
	// column — so " M" is "changed, not staged", "M " is "staged", "??" is
	// untracked, "UU" is a conflict. It is the vocabulary a person already
	// reads in `git status --short`, kept rather than re-invented.
	Status string `json:"status"`
	// Additions and Deletions count the file against HEAD, staged and unstaged
	// together. They are absent — not zero — when there is no number to give:
	// a binary file, a conflicted one, an untracked one too large to count.
	Additions *int `json:"additions,omitempty"`
	Deletions *int `json:"deletions,omitempty"`
	Binary    bool `json:"binary,omitempty"`
}

// Untracked reports whether git knows nothing about this path yet.
func (f File) Untracked() bool { return f.Status == "??" }

// ReadTree reads a repository's changed files, counts included.
//
// `git status -uall` is what makes the list a list of files: without it git
// folds a whole untracked directory into one row, and the counts below it would
// disagree with the rows above it.
func ReadTree(ctx context.Context, dir string) (Tree, error) {
	root, _, ok := groups.Find(dir)
	if !ok {
		return Tree{}, ErrNotARepository
	}
	ctx, cancel := context.WithTimeout(ctx, treeTimeout)
	defer cancel()

	snap, files, err := readStatus(ctx, dir)
	if err != nil {
		return Tree{}, err
	}
	snap.Root = root
	if err := readRest(ctx, dir, &snap); err != nil {
		return Tree{}, err
	}
	if err := addCounts(ctx, dir, root, snap.Head, files); err != nil {
		return Tree{}, err
	}
	return Tree{Snapshot: snap, Files: files}, nil
}

// addCounts fills each row's line counts. One diff answers for all tracked
// rows. An all-untracked batch needs no diff: none of its rows can occur in it.
func addCounts(ctx context.Context, dir, root, head string, files []File) error {
	return addCountsWith(ctx, dir, root, head, files, gitOut)
}

func addCountsWith(ctx context.Context, dir, root, head string, files []File, read gitRead) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stats map[string]numstat
	for _, f := range files {
		if f.Untracked() {
			continue
		}
		// The reference belongs to the preceding status observation. Do not
		// silently switch line-count baselines if HEAD moves during this read.
		args := []string{"-c", "core.quotepath=false", "diff", head, "--numstat", "-z", "--"}
		if head == "" {
			args = []string{"-c", "core.quotepath=false", "diff", "--cached", "--numstat", "-z", "--"}
		}
		out, err := read(ctx, dir, args...)
		if err != nil {
			return err
		}
		stats = parseNumstat(out)
		break
	}

	for i := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := &files[i]
		if s, ok := stats[f.Path]; ok {
			if s.binary {
				f.Binary = true
				continue
			}
			add, del := s.additions, s.deletions
			f.Additions, f.Deletions = &add, &del
			continue
		}
		if !f.Untracked() {
			continue // conflicts and the like: no number, no guess
		}
		lines, binary, ok := countLines(filepath.Join(root, f.Path))
		switch {
		case binary:
			f.Binary = true
		case ok:
			add, del := lines, 0
			f.Additions, f.Deletions = &add, &del
		}
	}
	return nil
}

// Git calls a file binary when its first 8000 bytes contain a NUL, and that is
// the rule here too: the two must agree about which files get no numbers.
const countSniffBytes = 8000

// countMaxBytes bounds the work one untracked file may cost. Past it there is
// no count — an honest absence beats a number that took a second to read.
const countMaxBytes = 4 << 20

// countLines counts the lines of an untracked file the way git's --numstat
// does: the last line counts even without a trailing newline (measured: the
// file "no-newline-at-end" is one line to git and zero to wc -l), and a NUL in
// the head makes it binary, which gets no count at all.
func countLines(path string) (lines int, binary, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, false
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, countMaxBytes+1))
	if err != nil || len(data) > countMaxBytes {
		return 0, false, false
	}
	head := data
	if len(head) > countSniffBytes {
		head = head[:countSniffBytes]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return 0, true, false
	}

	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lines++
	}
	return lines, false, true
}
