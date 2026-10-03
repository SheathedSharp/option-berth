package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// diffTimeout bounds one diff. A patch is bigger than a status and is read one
// file at a time; this is the ceiling on a single call, not on the whole read.
const diffTimeout = 15 * time.Second

// Line kinds. Exactly one of these per line of a Patch.
const (
	KindAdd     = "add"
	KindDel     = "del"
	KindContext = "context"
	KindHunk    = "hunk"
	KindMeta    = "meta"
)

// PatchLine is one line of a patch: the kind is derived from git's marker, and the
// text is what git wrote, marker included. Nothing is lost in the translation —
// a renderer prints the text, an agent reads it, and neither has to re-derive
// the format from the first character.
type PatchLine struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// FileDiff is one file's patch.
type FileDiff struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	// Binary is true when the patch has no hunks to read — git says the two
	// sides differ and stops.
	Binary bool        `json:"binary,omitempty"`
	Lines  []PatchLine `json:"lines"`
}

// Patch is the answer to "what changed, line by line".
type Patch struct {
	Root  string     `json:"root"`
	Files []FileDiff `json:"files"`
}

// ErrNoChanges is what Diff says about a path that exists but differs from HEAD
// in no way: an answer, not a failure — but still one the CLI reports, because
// silence would look like a hang.
var ErrNoChanges = errors.New("no changes to show for that path")

// ErrNoSuchFile is what Diff says about a path the repository does not have.
var ErrNoSuchFile = errors.New("no such path in the repository")

// Diff reads the patch for one path, or for everything that changed when file
// is empty.
//
// A single file costs a filtered status (to learn whether it is untracked, and
// whether it is a rename) and one diff. The whole repository costs one diff per
// changed file, because each `FileDiff` has to carry the path git gave — and a
// patch header is not a safe place to read a path from. Callers that know which
// file they want should name it.
func Diff(ctx context.Context, dir, file string) (Patch, error) {
	root, _, ok := groups.Find(dir)
	if !ok {
		return Patch{}, ErrNotARepository
	}
	ctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()

	if file != "" {
		f, unborn, err := changedFile(ctx, dir, root, file)
		if err != nil {
			return Patch{}, err
		}
		one, err := diffOne(ctx, dir, root, f, unborn)
		if err != nil {
			return Patch{}, err
		}
		return Patch{Root: root, Files: []FileDiff{one}}, nil
	}

	tree, err := ReadTree(ctx, dir)
	if err != nil {
		return Patch{}, err
	}
	patch := Patch{Root: root, Files: []FileDiff{}}
	for _, f := range tree.Files {
		one, err := diffOne(ctx, dir, root, f, tree.Head == "")
		if err != nil {
			return Patch{}, err
		}
		patch.Files = append(patch.Files, one)
	}
	return patch, nil
}

// changedFile finds the status row for one path.
//
// The whole listing is read rather than a filtered one, and that is a measured
// decision: given a pathspec, git stops detecting renames and reports a renamed
// file as a plain addition ("1 A." instead of "2 R." with the old path in the
// record after it), and it drops the branch headers a caller needs to know
// whether HEAD exists yet. The status of one file is not worth an answer that
// disagrees with the list the caller is looking at.
func changedFile(ctx context.Context, dir, root, file string) (File, bool, error) {
	rel, ok := resolvePath(root, file)
	if !ok {
		return File{}, false, ErrNoSuchFile
	}
	snap, files, err := readStatus(ctx, dir)
	if err != nil {
		return File{}, false, err
	}
	for _, f := range files {
		if f.Path == rel {
			return f, snap.Head == "", nil
		}
	}
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		return File{}, false, ErrNoSuchFile
	}
	return File{}, false, ErrNoChanges
}

// diffOne builds the patch for one row of a tree.
func diffOne(ctx context.Context, dir, root string, f File, unborn bool) (FileDiff, error) {
	out, err := diffText(ctx, dir, root, f, unborn)
	if err != nil {
		return FileDiff{}, err
	}
	lines := patchLines(string(out))
	return FileDiff{
		Path:    f.Path,
		OldPath: f.OldPath,
		Binary:  patchIsBinary(lines),
		Lines:   lines,
	}, nil
}

// diffArgs pin the shape of a patch: no colors, no external or textconv drivers,
// fixed a/ b/ prefixes, rename detection on. Callers add the revisions and the
// pathspec; the `-c core.quotepath=false` that goes with them is added by
// gitOut's callers (it is a git-wide option, so it comes before the verb).
var diffArgs = []string{"-M", "--patch", "--no-color", "--no-ext-diff", "--no-textconv",
	"--src-prefix=a/", "--dst-prefix=b/"}

// diffText asks git for one file's patch.
//
// The pathspec names both sides of a rename: measured — given only the new
// path, git reports a rename as a brand new file, and the two readers would
// disagree with the status row they are looking at.
func diffText(ctx context.Context, dir, root string, f File, unborn bool) ([]byte, error) {
	abs := filepath.Join(root, f.Path)
	if f.Untracked() {
		// An untracked file is in no diff. The empty tree is what it is a
		// change against, and --no-index exits 1 to mean "they differ".
		//
		// Asked from the repository root with a relative path, and not from
		// wherever the caller stands with an absolute one: --no-index prints
		// the paths it was given, and a header reading "a/private/tmp/…" would
		// not look like the patch of any other file.
		return gitOutDiff(ctx, root, "-c", "core.quotepath=false",
			"diff", "--no-index", "--", os.DevNull, f.Path)
	}

	args := []string{"-c", "core.quotepath=false", "diff"}
	if unborn {
		args = append(args, "--cached")
	} else {
		args = append(args, "HEAD")
	}
	args = append(args, diffArgs...)
	args = append(args, "--", abs)
	if f.OldPath != "" {
		args = append(args, filepath.Join(root, f.OldPath))
	}
	return gitOut(ctx, dir, args...)
}

// patchLines splits a patch into classified lines. An empty patch is an empty
// list, not a list with one empty line in it.
func patchLines(patch string) []PatchLine {
	text := strings.TrimSuffix(patch, "\n")
	if text == "" {
		return []PatchLine{}
	}
	raw := strings.Split(text, "\n")
	lines := make([]PatchLine, 0, len(raw))
	for _, one := range raw {
		lines = append(lines, PatchLine{Kind: lineKind(one), Text: one})
	}
	return lines
}

// lineKind reads the marker git puts at the head of every line of a patch. The
// file headers are checked before the added/removed lines they look like:
// "+++ b/x" is a header, not a line somebody added.
func lineKind(line string) string {
	switch {
	case strings.HasPrefix(line, "@@"):
		return KindHunk
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		return KindMeta
	case strings.HasPrefix(line, "+"):
		return KindAdd
	case strings.HasPrefix(line, "-"):
		return KindDel
	case strings.HasPrefix(line, " "):
		return KindContext
	}
	return KindMeta
}

// patchIsBinary reports whether git gave no hunks because the sides are binary.
func patchIsBinary(lines []PatchLine) bool {
	for _, l := range lines {
		if strings.HasPrefix(l.Text, "Binary files ") || strings.HasPrefix(l.Text, "GIT binary patch") {
			return true
		}
	}
	return false
}

// resolvePath reads a path the way this command documents it: relative to the
// repository root (the shape `oberth git files` prints), or absolute.
// Naming a file the way a shell in some subdirectory would is not supported on
// purpose — two candidate answers for one string is worse than one rule.
func resolvePath(root, file string) (string, bool) {
	if !filepath.IsAbs(file) {
		file = filepath.Join(root, file)
	}
	rel, err := filepath.Rel(root, filepath.Clean(file))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	// Git's repository-relative keys use '/' on Windows too. Convert only
	// after the native containment check; a literal backslash on Unix is not
	// a separator and must keep its filename meaning.
	return filepath.ToSlash(rel), true
}
