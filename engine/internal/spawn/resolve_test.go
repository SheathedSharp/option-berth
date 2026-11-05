package spawn

import (
	"os"
	"path/filepath"
	"testing"
)

// tempRepo builds a directory tree under a resolved temp dir. entries maps a
// relative path to its content; a path with no extension and empty content is
// a directory.
func tempRepo(t *testing.T, name string, entries map[string]string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for relative, body := range entries {
		writeEntry(t, root, relative, body)
	}
	return root
}

func writeEntry(t *testing.T, root, relative, body string) {
	t.Helper()
	target := filepath.Join(root, relative)
	if body == "" && filepath.Ext(relative) == "" {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveGroupAndName(t *testing.T) {
	repo := tempRepo(t, "shop-repo", map[string]string{".git": ""})

	configured := tempRepo(t, "workspace", map[string]string{
		".git": "",
		".oberth.yaml": "name: demo-app\n" +
			"services:\n" +
			"  - name: gateway\n" +
			"    cmd: uv run gateway app:app\n" +
			"    port: 8100\n" +
			"  - name: frontend\n" +
			"    cmd: pnpm run web\n" +
			"    cwd: frontend\n" +
			"    port: 5173\n",
		"frontend/.keep": "x",
	})

	loose := tempRepo(t, "loose-dir", nil)

	cases := []struct {
		label         string
		directory     string
		command       []string
		groupOverride string
		nameOverride  string
		group         string
		name          string
	}{
		{
			label:     "git root names the group, argv names the service",
			directory: repo, command: []string{"pnpm", "run", "web"},
			group: "shop-repo", name: "web",
		},
		{
			label:     "a nested directory still resolves to the checkout",
			directory: repo, command: []string{"./serve.sh"},
			group: "shop-repo", name: "serve.sh",
		},
		{
			label:     "the manifest wins over the directory name",
			directory: configured, command: []string{"uv", "run", "gateway", "app:app"},
			group: "demo-app", name: "gateway",
		},
		{
			label:     "a service with a cwd does not match from the root",
			directory: configured, command: []string{"pnpm", "run", "web"},
			group: "demo-app", name: "web",
		},
		{
			label:     "the service's own cwd matches it",
			directory: filepath.Join(configured, "frontend"), command: []string{"pnpm", "run", "web"},
			group: "demo-app", name: "frontend",
		},
		{
			label:     "no manifest and no checkout uses the directory name",
			directory: loose, command: []string{"python3", "-m", "http.server"},
			group: "loose-dir", name: "http.server",
		},
		{
			label:     "flags win over everything",
			directory: configured, command: []string{"uv", "run", "gateway", "app:app"},
			groupOverride: "itest", nameOverride: "frontend",
			group: "itest", name: "frontend",
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := Resolve(tc.directory, tc.command, tc.groupOverride, tc.nameOverride)
			if got.Group != tc.group || got.Name != tc.name {
				t.Fatalf("Resolve(%s, %q) = %q/%q, want %q/%q",
					tc.directory, tc.command, got.Group, got.Name, tc.group, tc.name)
			}
		})
	}
}

func TestResolveWorktreeGroupName(t *testing.T) {
	main := tempRepo(t, "demo-app", map[string]string{".git/worktrees/feature-x/.keep": "x"})
	worktree := tempRepo(t, "feature-x", map[string]string{
		".git": "gitdir: " + filepath.Join(main, ".git", "worktrees", "feature-x") + "\n",
	})
	got := Resolve(worktree, []string{"pnpm", "run", "web"}, "", "")
	if got.Group != "demo-app@feature-x" {
		t.Fatalf("worktree group = %q, want demo-app@feature-x", got.Group)
	}
}

// macOS $TMPDIR is the case: /var is a symlink to /private/var, so a run in a
// directory reached through a symlink must land in the same group as one in the
// directory itself, and the group must come from the manifest, not the link.
func TestResolveThroughASymlinkedCwd(t *testing.T) {
	real := tempRepo(t, "linked", map[string]string{
		".git":         "",
		".oberth.yaml": "name: linked-app\n",
	})
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	direct := Resolve(real, []string{"pnpm", "run", "web"}, "", "")
	if direct.Group != "linked-app" {
		t.Fatalf("direct group = %q, want linked-app", direct.Group)
	}

	viaLink := Resolve(link, []string{"pnpm", "run", "web"}, "", "")
	if viaLink.Group != "linked-app" {
		t.Fatalf("group through the link = %q, want linked-app", viaLink.Group)
	}
	if viaLink.ConfigPath != direct.ConfigPath {
		t.Fatalf("config through the link = %q, want %q", viaLink.ConfigPath, direct.ConfigPath)
	}
}

// With no manifest and no checkout the group is the directory the link points
// at, not the link's own name.
func TestResolveNamesTheRealDirectoryNotTheLink(t *testing.T) {
	real := tempRepo(t, "real-checkout", nil)
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got := Resolve(link, []string{"pnpm", "run", "web"}, "", "").Group; got != "real-checkout" {
		t.Fatalf("group = %q, want real-checkout", got)
	}
}
