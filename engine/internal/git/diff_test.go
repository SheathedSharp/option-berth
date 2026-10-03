package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The classification rule, including the trap: "+++"/"---" are file headers,
// not lines somebody added or removed.
func TestLineKindPrefersTheHeaders(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"diff --git a/x b/x", KindMeta},
		{"index b1e0723..143b93d 100644", KindMeta},
		{"--- a/x", KindMeta},
		{"+++ b/x", KindMeta},
		{"@@ -1,3 +1,4 @@", KindHunk},
		{"+added", KindAdd},
		{"-removed", KindDel},
		{" kept", KindContext},
		{" ", KindContext},
		{"\\ No newline at end of file", KindMeta},
		{"Binary files a/x and b/x differ", KindMeta},
	}
	for _, c := range cases {
		if got := lineKind(c.line); got != c.want {
			t.Errorf("lineKind(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// An empty patch is an empty list, not a list holding one empty line.
func TestPatchLinesOfAnEmptyPatch(t *testing.T) {
	if got := patchLines(""); len(got) != 0 {
		t.Errorf("patchLines(\"\") = %+v, want none", got)
	}
}

func TestDiffOfOneFile(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "kept.txt", "one\ntwo\nthree\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	write(t, dir, "kept.txt", "one\nTWO\nthree\nfour\n")

	patch, err := Diff(context.Background(), dir, "kept.txt")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if patch.Root == "" {
		t.Error("root is empty")
	}
	if len(patch.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(patch.Files))
	}
	fd := patch.Files[0]
	if fd.Path != "kept.txt" || fd.Binary {
		t.Fatalf("file = %+v", fd)
	}

	kinds := map[string]int{}
	texts := []string{}
	for _, l := range fd.Lines {
		kinds[l.Kind]++
		texts = append(texts, l.Text)
	}
	joined := strings.Join(texts, "\n")

	if kinds[KindHunk] != 1 {
		t.Errorf("hunks = %d, want 1: %q", kinds[KindHunk], joined)
	}
	if kinds[KindAdd] != 2 || kinds[KindDel] != 1 {
		t.Errorf("add/del = %d/%d, want 2/1: %q", kinds[KindAdd], kinds[KindDel], joined)
	}
	if kinds[KindContext] == 0 || kinds[KindMeta] == 0 {
		t.Errorf("context/meta missing: %q", joined)
	}
	// The text is git's own, markers included: a renderer prints it as it is.
	if !strings.Contains(joined, "+four") {
		t.Errorf("the added line is not verbatim in the patch: %q", joined)
	}
	if !strings.Contains(joined, "-two") {
		t.Errorf("the removed line is not verbatim in the patch: %q", joined)
	}
}

// A rename is the case a path filter can lose: measured — asked for by its new
// path alone, git reports a different file altogether, and the reader would
// disagree with the status row it is looking at.
func TestDiffOfARenamedFileKeepsTheRename(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "old-name.txt", "one\ntwo\nthree\nfour\nfive\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	run(t, dir, "mv", "old-name.txt", "new-name.txt")

	patch, err := Diff(context.Background(), dir, "new-name.txt")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	fd := patch.Files[0]
	if fd.OldPath != "old-name.txt" {
		t.Errorf("old_path = %q, want old-name.txt", fd.OldPath)
	}
	joined := linesText(fd)
	if !strings.Contains(joined, "rename from old-name.txt") || !strings.Contains(joined, "rename to new-name.txt") {
		t.Errorf("the patch does not carry the rename: %q", joined)
	}
}

// An untracked file is in no diff: it is shown against the empty tree, and
// every line of it is an addition.
func TestDiffOfAnUntrackedFile(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "kept.txt", "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	write(t, dir, "fresh.txt", "new\nfile\n")

	patch, err := Diff(context.Background(), dir, "fresh.txt")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	fd := patch.Files[0]
	if fd.Binary {
		t.Error("fresh.txt is not binary")
	}
	joined := linesText(fd)
	if !strings.Contains(joined, "new file mode 100644") {
		t.Errorf("not shown as a new file: %q", joined)
	}
	for _, l := range fd.Lines {
		if l.Kind == KindAdd {
			continue
		}
		if l.Kind == KindMeta || l.Kind == KindHunk {
			continue
		}
		t.Errorf("a line of a new file is %q: %q", l.Kind, l.Text)
	}
}

// A file the repository has, with nothing changed about it, is an answer and
// not a failure — but the caller has to hear it, or it waits forever.
func TestDiffOfAnUnchangedFile(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "kept.txt", "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	_, err := Diff(context.Background(), dir, "kept.txt")
	if !errors.Is(err, ErrNoChanges) {
		t.Fatalf("err = %v, want ErrNoChanges", err)
	}
}

func TestDiffOfAPathTheRepositoryDoesNotHave(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "kept.txt", "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	if _, err := Diff(context.Background(), dir, "nope.txt"); !errors.Is(err, ErrNoSuchFile) {
		t.Errorf("err = %v, want ErrNoSuchFile", err)
	}
	if _, err := Diff(context.Background(), dir, "../outside.txt"); !errors.Is(err, ErrNoSuchFile) {
		t.Errorf("a path outside the repository: err = %v, want ErrNoSuchFile", err)
	}
}

// The whole repository is one entry per changed file, each carrying its own
// path — a patch header is not a place to read a path from.
func TestDiffWholeRepository(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "one.txt", "one\n")
	write(t, dir, "two.txt", "two\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	write(t, dir, "one.txt", "one changed\n")
	write(t, dir, "two.txt", "two changed\n")
	write(t, dir, "three.txt", "three is new\n")

	patch, err := Diff(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(patch.Files) != 3 {
		t.Fatalf("got %d files, want 3: %+v", len(patch.Files), patch.Files)
	}
	paths := map[string]bool{}
	for _, f := range patch.Files {
		paths[f.Path] = true
		if len(f.Lines) == 0 {
			t.Errorf("%s came back with no lines", f.Path)
		}
	}
	for _, want := range []string{"one.txt", "two.txt", "three.txt"} {
		if !paths[want] {
			t.Errorf("%s is missing from %v", want, paths)
		}
	}
}

// A clean checkout has nothing to show, and that is an empty list rather than
// an error: the two readers both print the empty answer.
func TestDiffOfACleanRepository(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "kept.txt", "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	patch, err := Diff(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if patch.Files == nil || len(patch.Files) != 0 {
		t.Fatalf("files = %+v, want an empty list", patch.Files)
	}
}

// Paths are named relative to the repository root — the shape the file list
// prints — and absolute paths are taken as they are.
func TestDiffResolvesPathsFromTheRoot(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "sub/deep.txt", "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	write(t, dir, "sub/deep.txt", "two\n")

	patch, err := Diff(context.Background(), filepath.Join(dir, "sub"), "sub/deep.txt")
	if err != nil {
		t.Fatalf("Diff from a subdirectory: %v", err)
	}
	if patch.Files[0].Path != "sub/deep.txt" {
		t.Errorf("path = %q, want sub/deep.txt", patch.Files[0].Path)
	}

	abs, err := Diff(context.Background(), dir, filepath.Join(dir, "sub/deep.txt"))
	if err != nil {
		t.Fatalf("Diff with an absolute path: %v", err)
	}
	if abs.Files[0].Path != "sub/deep.txt" {
		t.Errorf("path = %q, want sub/deep.txt", abs.Files[0].Path)
	}
}

func TestDiffOutsideARepository(t *testing.T) {
	if _, err := Diff(context.Background(), t.TempDir(), ""); !errors.Is(err, ErrNotARepository) {
		t.Fatalf("err = %v, want ErrNotARepository", err)
	}
}

// A binary file has no hunks to read: git says the sides differ and stops, and
// the reader says so rather than inventing a rendering.
func TestDiffOfABinaryFile(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "logo.bin", "\x00\x01first\x00")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	write(t, dir, "logo.bin", "\x00\x01second\x00")

	patch, err := Diff(context.Background(), dir, "logo.bin")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	fd := patch.Files[0]
	if !fd.Binary {
		t.Errorf("file = %+v, want binary", fd)
	}
	if !strings.Contains(linesText(fd), "Binary files") {
		t.Errorf("git's own line is missing: %q", linesText(fd))
	}
}

func linesText(fd FileDiff) string {
	texts := make([]string, 0, len(fd.Lines))
	for _, l := range fd.Lines {
		texts = append(texts, l.Text)
	}
	return strings.Join(texts, "\n")
}

func mkdirAll(t *testing.T, path string) error {
	t.Helper()
	return os.MkdirAll(path, 0o755)
}
