package spawn

import (
	"path/filepath"
	"testing"
)

// A linked worktree that carries a copy of the main checkout's committed
// `.oberth.yaml` still records the worktree's own group, named by the main
// checkout — not the copy's `name:`, and not the main checkout's group.
//
// The copied manifest is why the group cannot simply be "whatever name: says":
// a checkout that committed a per-machine value would otherwise rename the
// worktree along with itself.
func TestResolveWorktreeWithACopiedConfig(t *testing.T) {
	main := project(t, "shop-dir", `
.git/worktrees/feat/
.oberth.yaml
  name: shop
  services:
    - name: web
      cmd: npm run dev
`)
	worktree := project(t, "feat", `
.git
  gitdir: `+filepath.Join(main, ".git", "worktrees", "feat")+`
.oberth.yaml
  name: stale
  services:
    - name: web
      cmd: npm run dev
`)

	got := Resolve(worktree, []string{"npm", "run", "dev"}, "", "")
	if got.Group != "shop@feat" || got.Name != "web" {
		t.Fatalf("worktree run = %q/%q, want shop@feat/web", got.Group, got.Name)
	}
	if want := filepath.Join(worktree, ".oberth.yaml"); got.ConfigPath != want {
		t.Errorf("config path = %q, want %q (the worktree's own copy)", got.ConfigPath, want)
	}

	if inMain := Resolve(main, []string{"npm", "run", "dev"}, "", ""); inMain.Group != "shop" {
		t.Errorf("main checkout run group = %q, want shop", inMain.Group)
	}
}
