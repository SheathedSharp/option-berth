package groups

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Until the PEB reader landed, no port on Windows had a cwd, so nothing in this
// package was ever asked a question shaped like `C:\src\api`. These tests ask
// it. The first two run everywhere — filepath is the platform's own separator,
// so on the Windows runner they are the drive-letter case and on other systems
// they are the regression guard — and the last two are the literal backslash
// contract, which only means anything where backslash is the separator.

func requireFind(t *testing.T, cwd, wantRoot, wantWorktree, wantGroup string) {
	t.Helper()
	root, worktree, ok := Find(cwd)
	if !ok {
		t.Errorf("Find(%q) found no checkout", cwd)
		return
	}
	if root != wantRoot || worktree != wantWorktree {
		t.Errorf("Find(%q) = root %q worktree %q, want %q / %q", cwd, root, worktree, wantRoot, wantWorktree)
	}
	if got := GroupName(root, worktree); got != wantGroup {
		t.Errorf("GroupName for %q = %q, want %q", cwd, got, wantGroup)
	}
}

func requireSameCanonical(t *testing.T, want string, spellings ...string) {
	t.Helper()
	for _, spelling := range spellings {
		if got := Canonical(spelling); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", spelling, got, want)
		}
	}
}

func requireWindows(t *testing.T, reason string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip(reason)
	}
}

func TestFindAcceptsEverySpellingOfTheSameCheckout(t *testing.T) {
	base := tempTree(t)
	clone := mkdir(t, base, "gateway")
	mkdir(t, clone, ".git")
	deep := mkdir(t, clone, "services", "web")
	sep := string(filepath.Separator)
	wantRoot := Canonical(clone)
	if wantRoot == "" {
		t.Fatal("Canonical returned nothing for a checkout that exists")
	}

	spellings := []string{
		clone,
		clone + sep,
		filepath.Join(clone, "."),
		deep,
		deep + sep,
		filepath.Join(deep, "..", "..", "services", "web"),
	}
	for _, cwd := range spellings {
		requireFind(t, cwd, wantRoot, "", "gateway")
	}

	root, worktree, ok := Find("")
	if ok || root != "" || worktree != "" {
		t.Errorf("Find(\"\") = %q, %q, %v; want no checkout", root, worktree, ok)
	}
}

func TestCanonicalCollapsesTheSpellingsToOneKey(t *testing.T) {
	base := tempTree(t)
	dir := mkdir(t, base, "api", "services", "web")
	sep := string(filepath.Separator)

	want := Canonical(dir)
	if want == "" {
		t.Fatal("Canonical returned nothing for a directory that exists")
	}
	if !filepath.IsAbs(want) {
		t.Errorf("Canonical(%q) = %q, want an absolute key", dir, want)
	}
	if strings.HasSuffix(want, sep) && filepath.Dir(want) != want {
		t.Errorf("Canonical(%q) = %q, which keeps a trailing separator", dir, want)
	}
	requireSameCanonical(t, want,
		dir+sep,
		filepath.Join(dir, "."),
		filepath.Join(dir, "..", "web"),
	)
	if got := Canonical(""); got != "" {
		t.Errorf("Canonical(\"\") = %q, want empty", got)
	}
}

func TestWindowsPathShapes(t *testing.T) {
	requireWindows(t, "drive letters and backslashes are only path syntax on Windows")
	base := tempTree(t)
	clone := mkdir(t, base, "gateway")
	mkdir(t, clone, ".git")
	deep := mkdir(t, clone, "services", "web")

	drive := filepath.VolumeName(clone)
	if drive == "" || !strings.Contains(clone, `\`) {
		t.Fatalf("the temp checkout %q is not a drive-letter path", clone)
	}

	// The PEB hands back a backslash path, while a config or an env var may
	// spell the same directory with forward slashes; Windows accepts both.
	forward := strings.ReplaceAll(deep, `\`, `/`)
	canonical := Canonical(deep)
	requireSameCanonical(t, canonical, forward)
	if strings.Contains(canonical, "/") {
		t.Errorf("Canonical(%q) = %q, which still contains a forward slash", deep, canonical)
	}

	// The drive letter's case does not make a second project either.
	lower := strings.ToLower(clone[:1]) + clone[1:]
	upper := strings.ToUpper(clone[:1]) + clone[1:]
	requireSameCanonical(t, Canonical(upper), lower)

	// Find walks up with filepath.Dir, which has to stop at the drive root
	// rather than loop, and GroupName has to take the directory name off a
	// backslash path.
	requireFind(t, forward, Canonical(clone), "", "gateway")

	// A directory outside any checkout still terminates: the walk up from a
	// drive root has nowhere left to go.
	outside := drive + `\`
	if _, _, ok := Find(outside); ok {
		if _, err := os.Stat(filepath.Join(outside, ".git")); err != nil {
			t.Errorf("Find(%q) claimed a checkout at the drive root", outside)
		}
	}
}

func TestWindowsWorktreeGitFile(t *testing.T) {
	requireWindows(t, "backslash gitdir lines only occur on Windows")
	base := tempTree(t)
	clone := mkdir(t, base, "option-berth")
	mkdir(t, clone, ".git")
	wt := mkdir(t, base, "option-berth-feature")
	writeFile(t, filepath.Join(wt, ".git"),
		"gitdir: "+filepath.Join(clone, ".git", "worktrees", "option-berth-feature")+"\r\n")

	const qualified = "option-berth@option-berth-feature"
	requireFind(t, wt, Canonical(wt), qualified, qualified)
}
