package groups

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// tempTree returns a temp dir with symlinks resolved, because Find resolves
// them and macOS puts t.TempDir() behind /var -> /private/var.
func tempTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireFindCheckout(t *testing.T, name, cwd, wantRoot, wantWorktree string, wantOK bool, wantGroup string, want Checkout) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		root, worktree, ok := Find(cwd)
		if root != wantRoot || worktree != wantWorktree || ok != wantOK {
			t.Fatalf("Find(%q) = (%q, %q, %v), want (%q, %q, %v)",
				cwd, root, worktree, ok, wantRoot, wantWorktree, wantOK)
		}
		if got := GroupName(root, worktree); got != wantGroup {
			t.Fatalf("GroupName(%q, %q) = %q, want %q", root, worktree, got, wantGroup)
		}
		co, found := Locate(cwd)
		if found != wantOK {
			t.Fatalf("Locate(%q) found = %v, want %v", cwd, found, wantOK)
		}
		if !reflect.DeepEqual(co, want) {
			t.Fatalf("Locate(%q) = %#v, want %#v", cwd, co, want)
		}
		if found && co.Linked() != (wantWorktree != "") {
			t.Fatalf("Locate(%q).Linked() = %v, disagrees with Find worktree %q", cwd, co.Linked(), wantWorktree)
		}
	})
}

func TestFind(t *testing.T) {
	base := tempTree(t)

	// A normal clone with a .git directory and a nested source dir.
	clone := mkdir(t, base, "clone")
	mkdir(t, clone, ".git")
	cloneDeep := mkdir(t, clone, "backend", "app")

	// A repository nested inside another repository.
	nested := mkdir(t, clone, "vendor", "inner")
	mkdir(t, nested, ".git")

	// A linked worktree: .git is a file pointing into the main repo's
	// worktrees/ directory.
	wt := mkdir(t, base, "worktrees", "feature-x")
	writeFile(t, filepath.Join(wt, ".git"),
		"gitdir: "+filepath.Join(clone, ".git", "worktrees", "feature-x")+"\n")
	wtDeep := mkdir(t, wt, "frontend")

	// A worktree whose .git file uses a relative gitdir.
	relwt := mkdir(t, base, "relwt")
	rel, err := filepath.Rel(relwt, filepath.Join(clone, ".git", "worktrees", "relwt"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(relwt, ".git"), "gitdir: "+rel+"\n")

	// A submodule: .git file into .git/modules, not a worktree.
	sub := mkdir(t, base, "sub")
	writeFile(t, filepath.Join(sub, ".git"),
		"gitdir: "+filepath.Join(clone, ".git", "modules", "sub")+"\n")

	// A .git file that says nothing useful.
	junk := mkdir(t, base, "junk")
	writeFile(t, filepath.Join(junk, ".git"), "not a gitdir line\n")

	outside := mkdir(t, base, "outside", "deep")

	main := func(root string) Checkout {
		return Checkout{Root: root, Main: root, head: filepath.Join(root, ".git", "HEAD")}
	}
	linked := func(root, name string) Checkout {
		return Checkout{
			Root:     root,
			Main:     clone,
			Worktree: name,
			head:     filepath.Join(clone, ".git", "worktrees", name, "HEAD"),
		}
	}

	requireFindCheckout(t, "git dir at the root", clone, clone, "", true, "clone", main(clone))
	requireFindCheckout(t, "git dir from a nested cwd", cloneDeep, clone, "", true, "clone", main(clone))
	requireFindCheckout(t, "nested repo wins over its parent", nested, nested, "", true, "inner", main(nested))
	requireFindCheckout(t, "worktree is qualified", wt, wt, "clone@feature-x", true, "clone@feature-x", linked(wt, "feature-x"))
	requireFindCheckout(t, "worktree from a nested cwd", wtDeep, wt, "clone@feature-x", true, "clone@feature-x", linked(wt, "feature-x"))
	requireFindCheckout(t, "worktree with a relative gitdir", relwt, relwt, "clone@relwt", true, "clone@relwt", linked(relwt, "relwt"))
	requireFindCheckout(t, "submodule is not a worktree", sub, sub, "", true, "sub", Checkout{
		Root: sub, Main: sub, head: filepath.Join(clone, ".git", "modules", "sub", "HEAD"),
	})
	requireFindCheckout(t, "unparseable git file", junk, junk, "", true, "junk", Checkout{Root: junk, Main: junk})
	requireFindCheckout(t, "outside any repo", outside, "", "", false, "", Checkout{})
	requireFindCheckout(t, "empty cwd", "", "", "", false, "", Checkout{})

	if Canonical(clone) != clone {
		t.Fatalf("Find uses Canonical roots; Canonical(%q) = %q", clone, Canonical(clone))
	}
	wantRel := filepath.Join("..", "clone", ".git", "worktrees", "relwt")
	if rel != wantRel {
		t.Fatalf("relative gitdir fixture = %q, want %q", rel, wantRel)
	}
	gotRel, err := os.ReadFile(filepath.Join(relwt, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotRel) != "gitdir: "+wantRel+"\n" {
		t.Fatalf("relative .git file = %q", gotRel)
	}
	if target := gitdirTarget(relwt, filepath.Join(relwt, ".git")); target != filepath.Join(clone, ".git", "worktrees", "relwt") {
		t.Fatalf("gitdirTarget(relwt) = %q, want the absolute worktree admin path", target)
	}
	if target := gitdirTarget(junk, filepath.Join(junk, ".git")); target != "" {
		t.Fatalf("gitdirTarget(junk) = %q, want empty", target)
	}
	if mainRoot := worktreeMain(filepath.Join(clone, ".git", "worktrees", "feature-x")); mainRoot != clone {
		t.Fatalf("worktreeMain(feature-x admin) = %q, want %q", mainRoot, clone)
	}
	if mainRoot := worktreeMain(filepath.Join(clone, ".git", "modules", "sub")); mainRoot != "" {
		t.Fatalf("worktreeMain(submodule admin) = %q, want empty", mainRoot)
	}
}
