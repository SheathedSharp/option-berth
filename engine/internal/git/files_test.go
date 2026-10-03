package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The sample is copied from a real `git status --porcelain=v2 --branch -uall -z`
// run over a fixture that had one of everything (git 2.50.1), NULs written as
// \x00. Every record shape the reader claims to understand is in it.
func TestParseStatusReadsRows(t *testing.T) {
	const out = "# branch.oid fda64dfa900f4fc27ce70fe535aa871c4b8bfc1c\x00" +
		"# branch.head main\x00" +
		"1 .M N... 100644 100644 100644 d913645f4b3a5250330e571143292dff8b8b402c d913645f4b3a5250330e571143292dff8b8b402c bin.dat\x00" +
		"2 R. N... 100644 100644 100644 b77b4eb1d946f923f61785536da9ca5af6909f06 b77b4eb1d946f923f61785536da9ca5af6909f06 R100 ren-new.txt\x00ren-old.txt\x00" +
		"u UU N... 100644 100644 100644 100644 d85166490bde1a3aebd06c792801d1b83421458c ba2906d0666cf726c7eaadd2cd3db615dedfdf3a 2299c37978265a95cbe835a4b0f0bbf15aad5549 conflict.txt\x00" +
		"1 A. N... 000000 100644 100644 0000000000000000000000000000000000000000 5786b13ddf42af9492f8f94243b3397cded5d953 staged-new.txt\x00" +
		"? fresh.txt\x00" +
		"? newdir/inner.txt\x00"

	var s Snapshot
	files := parseStatus([]byte(out), &s)

	want := []File{
		{Path: "bin.dat", Status: " M"}, // unstaged, "." became a space
		{Path: "ren-new.txt", OldPath: "ren-old.txt", Status: "R "},
		{Path: "conflict.txt", Status: "UU"},
		{Path: "staged-new.txt", Status: "A "},
		{Path: "fresh.txt", Status: "??"},
		{Path: "newdir/inner.txt", Status: "??"},
	}
	if len(files) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(files), len(want), files)
	}
	for i, w := range want {
		if files[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, files[i], w)
		}
	}

	// The counts have to describe the same rows: one staged rename and one
	// staged new file, one unstaged file, two untracked, one conflict.
	if s.Staged != 2 || s.Unstaged != 1 || s.Untracked != 2 || s.Conflicts != 1 {
		t.Errorf("counts = %d staged / %d unstaged / %d untracked / %d conflicts, want 2/1/2/1",
			s.Staged, s.Unstaged, s.Untracked, s.Conflicts)
	}
}

// A path is one field and it may contain spaces: the record's own columns are
// counted, and everything after them is the name. Measured — a file called
// "with space.txt" is the case a naive split truncates to "with".
func TestParseStatusKeepsSpacesInPaths(t *testing.T) {
	const out = "1 .M N... 100644 100644 100644 2fa992c0b8b5c6acd2bdd4fa31de29d29799bdd5 fe5841d90e218ad805244a4c5770601ae956987f with space.txt\x00" +
		"2 R. N... 100644 100644 100644 b77b4eb1d946f923f61785536da9ca5af6909f06 b77b4eb1d946f923f61785536da9ca5af6909f06 R100 renamed file.txt\x00its old name.txt\x00" +
		"u UU N... 100644 100644 100644 100644 d85166490bde1a3aebd06c792801d1b83421458c ba2906d0666cf726c7eaadd2cd3db615dedfdf3a 2299c37978265a95cbe835a4b0f0bbf15aad5549 a conflict.txt\x00"

	var s Snapshot
	files := parseStatus([]byte(out), &s)

	want := []File{
		{Path: "with space.txt", Status: " M"},
		{Path: "renamed file.txt", OldPath: "its old name.txt", Status: "R "},
		{Path: "a conflict.txt", Status: "UU"},
	}
	if len(files) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(files), len(want), files)
	}
	for i, w := range want {
		if files[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, files[i], w)
		}
	}
}

// A rename is the one record whose paths are two records: measured — the path
// field is empty and the old and the new path follow as records of their own.
func TestParseNumstatReadsTheSisterRecords(t *testing.T) {
	const out = "50\t0\tengine/internal/config/config.go\x00" +
		"-\t-\tbin.dat\x00" +
		"0\t0\t\x00ren-old.txt\x00ren-new.txt\x00" +
		"4\t0\tconflict.txt\x00"

	stats := parseNumstat([]byte(out))

	if got := stats["engine/internal/config/config.go"]; got.additions != 50 || got.deletions != 0 || got.binary {
		t.Errorf("config.go = %+v", got)
	}
	if got := stats["bin.dat"]; !got.binary {
		t.Errorf("bin.dat = %+v, want binary", got)
	}
	if got, ok := stats["ren-new.txt"]; !ok || got.additions != 0 || got.deletions != 0 {
		t.Errorf("the rename should be keyed by its new path: %+v", stats)
	}
	if _, ok := stats["ren-old.txt"]; ok {
		t.Error("the old path should not be a key of its own")
	}
	if got := stats["conflict.txt"]; got.additions != 4 {
		t.Errorf("conflict.txt = %+v", got)
	}
}

// The three things a row has to get right, against a real repository: which file
// it is, which side it is on, and how much of it moved.
func TestReadTreeListsWhatChanged(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "kept.txt", "one\ntwo\nthree\n")
	write(t, dir, "staged.txt", "a\nb\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	write(t, dir, "kept.txt", "one\nTWO\nthree\nfour\n") // +2 -1 against HEAD
	write(t, dir, "staged.txt", "a\nb\nc\n")             // +1
	run(t, dir, "add", "staged.txt")
	write(t, dir, "fresh.txt", "new\nfile\n") // untracked, two lines

	tree, err := ReadTree(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	if len(tree.Files) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(tree.Files), tree.Files)
	}

	kept := findFile(t, tree, "kept.txt")
	if kept.Status != " M" {
		t.Errorf("kept.txt status = %q, want \" M\"", kept.Status)
	}
	if add, del := numbers(t, kept); add != 2 || del != 1 {
		t.Errorf("kept.txt counts = +%d -%d, want +2 -1", add, del)
	}

	staged := findFile(t, tree, "staged.txt")
	if staged.Status != "M " {
		t.Errorf("staged.txt status = %q, want \"M \"", staged.Status)
	}
	if add, del := numbers(t, staged); add != 1 || del != 0 {
		t.Errorf("staged.txt counts = +%d -%d, want +1 -0", add, del)
	}

	fresh := findFile(t, tree, "fresh.txt")
	if !fresh.Untracked() {
		t.Errorf("fresh.txt status = %q, want untracked", fresh.Status)
	}
	if add, del := numbers(t, fresh); add != 2 || del != 0 {
		t.Errorf("fresh.txt counts = +%d -%d, want +2 -0 (counted from the file)", add, del)
	}

	if tree.Staged != 1 || tree.Unstaged != 1 || tree.Untracked != 1 {
		t.Errorf("counts = %d/%d/%d, want 1/1/1", tree.Staged, tree.Unstaged, tree.Untracked)
	}
	if tree.Root == "" || tree.Branch == "" {
		t.Errorf("the overview should come along: root %q branch %q", tree.Root, tree.Branch)
	}
}

// A directory of untracked files is that many rows. Without -uall git folds it
// into one record and the counts above the list stop describing the list.
func TestReadTreeSeesEveryFileInAnUntrackedDirectory(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "kept.txt", "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	if err := os.MkdirAll(filepath.Join(dir, "dist", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "dist/a.js", "a\n")
	write(t, dir, "dist/b.js", "b\n")
	write(t, dir, "dist/nested/c.js", "c\n")

	tree, err := ReadTree(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	if len(tree.Files) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(tree.Files), tree.Files)
	}
	if tree.Untracked != 3 {
		t.Errorf("untracked count = %d, want 3 files (not one directory)", tree.Untracked)
	}
}

// A binary file gets no count at all — an absent number, not a wrong one.
func TestReadTreeBinaryHasNoCounts(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "kept.txt", "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	write(t, dir, "logo.bin", "\x00\x01binary\x00data")

	tree, err := ReadTree(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	logo := findFile(t, tree, "logo.bin")
	if !logo.Binary {
		t.Errorf("logo.bin = %+v, want binary", logo)
	}
	if logo.Additions != nil || logo.Deletions != nil {
		t.Errorf("logo.bin has counts %v/%v, want none", logo.Additions, logo.Deletions)
	}
}

// git counts the last line even when it has no newline; wc -l does not. The two
// numbers have to agree with git's, because that is the number the file list
// shows next to `git status`'s own answer.
func TestCountLinesAgreesWithGit(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)

	names := map[string]string{
		"three.txt":    "one\ntwo\nthree\n",
		"no-newline":   "one\ntwo",
		"one-line":     "only\n",
		"empty.txt":    "",
		"blank-lines":  "\n\n\n",
		"crlf.txt":     "one\r\ntwo\r\n",
		"long-line":    strings.Repeat("x", 5000) + "\n",
		"with-nul.bin": "text\x00more\n",
	}
	for name, content := range names {
		write(t, dir, name, content)
	}

	for _, name := range sortedKeys(names) {
		path := filepath.Join(dir, name)
		lines, binary, ok := countLines(path)

		want, wantBinary := gitNumstat(t, path)
		if wantBinary {
			if !binary || ok {
				t.Errorf("%s: binary = %v ok = %v, want binary", name, binary, ok)
			}
			continue
		}
		if binary || !ok {
			t.Errorf("%s: binary = %v ok = %v, want a count", name, binary, ok)
			continue
		}
		if lines != want {
			t.Errorf("%s: counted %d lines, git says %d", name, lines, want)
		}
	}
}

// An untracked file is in no diff, so its count is read from the file itself —
// and the absence of a trailing newline must not lose the last line.
func TestReadTreeCountsAnUntrackedFileWithoutATrailingNewline(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "kept.txt", "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	write(t, dir, "nonl.txt", "no-newline-at-end")

	tree, err := ReadTree(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	row := findFile(t, tree, "nonl.txt")
	if add, del := numbers(t, row); add != 1 || del != 0 {
		t.Errorf("counts = +%d -%d, want +1 -0", add, del)
	}
}

// A repository with no commit yet has no HEAD to diff against; the index
// against the empty tree is what "changed" means there.
func TestReadTreeOnAnEmptyRepository(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "new.txt", "one\ntwo\n")
	run(t, dir, "add", ".")

	tree, err := ReadTree(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	row := findFile(t, tree, "new.txt")
	if row.Status != "A " {
		t.Errorf("status = %q, want \"A \"", row.Status)
	}
	if add, del := numbers(t, row); add != 2 || del != 0 {
		t.Errorf("counts = +%d -%d, want +2 -0", add, del)
	}
}

func TestReadTreeOutsideARepository(t *testing.T) {
	_, err := ReadTree(context.Background(), t.TempDir())
	if err != ErrNotARepository {
		t.Fatalf("err = %v, want ErrNotARepository", err)
	}
}

// A clean checkout has no rows, not a nil list: the two readers both print an
// empty answer, and null is not one.
func TestReadTreeOnACleanRepositoryIsAnEmptyList(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "kept.txt", "one\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	tree, err := ReadTree(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	if tree.Files == nil {
		t.Error("files is nil, want an empty list")
	}
	if len(tree.Files) != 0 {
		t.Errorf("got %d rows on a clean checkout", len(tree.Files))
	}
}

func findFile(t *testing.T, tree Tree, path string) File {
	t.Helper()
	for _, f := range tree.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("%s is not in %+v", path, tree.Files)
	return File{}
}

func numbers(t *testing.T, f File) (int, int) {
	t.Helper()
	if f.Additions == nil || f.Deletions == nil {
		t.Fatalf("%s has no counts: %+v", f.Path, f)
	}
	return *f.Additions, *f.Deletions
}

// gitNumstat asks git itself what a file's count is, the way the file list
// would show it: a new file against the empty tree. git exits 1 when the two
// sides differ, which is the normal answer here.
func gitNumstat(t *testing.T, path string) (int, bool) {
	t.Helper()
	out, err := exec.Command("git", "diff", "--no-index", "--numstat", "--", os.DevNull, path).Output()
	if err != nil && len(out) == 0 {
		t.Fatalf("git diff --no-index: %v", err)
	}
	fields := strings.SplitN(strings.TrimSpace(string(out)), "\t", 3)
	if len(fields) < 2 || fields[0] == "-" {
		return 0, true
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("numstat %q: %v", out, err)
	}
	return n, false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
