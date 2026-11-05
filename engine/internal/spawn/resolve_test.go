package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// project materialises a throwaway directory tree and returns its resolved
// path. The tree is written as one multi-line string — a path ending in "/" is a
// directory, anything else is a file holding the lines under it — so a fixture
// reads as the shape it builds instead of as a chain of MkdirAll calls.
//
//	project(t, "shop", `
//	.git
//	.oberth.yaml
//	  name: shop
//	`)
func project(t *testing.T, name, layout string) string {
	t.Helper()
	root := filepath.Join(resolvedTemp(t), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating %s: %v", root, err)
	}
	var current string // the file the current indented block belongs to
	for _, line := range strings.Split(strings.TrimPrefix(layout, "\n"), "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			continue
		case strings.HasPrefix(line, "  "):
			if current == "" {
				t.Fatalf("indented line with no file above it: %q", line)
			}
			appendLine(t, filepath.Join(root, current), strings.TrimPrefix(line, "  "))
		case strings.HasSuffix(line, "/"):
			if err := os.MkdirAll(filepath.Join(root, line), 0o755); err != nil {
				t.Fatal(err)
			}
			current = ""
		default:
			current = line
			path := filepath.Join(root, current)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// resolvedTemp is a temp directory with symlinks already followed, so a fixture
// path compares equal to what the resolver computes on a host where the temp
// root is itself a link (macOS /var against /private/var).
func resolvedTemp(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temp directory: %v", err)
	}
	return resolved
}

// appendLine adds one line to an already created fixture file.
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// link creates a symlink to target and skips the test where the platform does
// not allow one.
func link(t *testing.T, target string) string {
	t.Helper()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return alias
}

// A checkout without a manifest, a checkout with one, and a plain directory give
// the three rungs of the group chain; a flag or a declared service gives the two
// rungs of the name chain.
func TestResolveGroupAndName(t *testing.T) {
	bareCheckout := project(t, "my-repo", `
.git
`)
	configured := project(t, "checkout", `
.git
.oberth.yaml
  name: option-berth
  services:
    - name: api
      cmd: uv run uvicorn app:app
      port: 8000
    - name: web
      cmd: npm run dev
      cwd: frontend
      port: 5173
frontend/
`)
	plainDir := project(t, "just-a-dir", "")

	cases := []struct {
		desc  string
		cwd   string
		argv  []string
		group string // "" means no --group flag
		name  string // "" means no --name flag
		want  Resolution
	}{
		{
			desc: "the checkout names the group, the command names the service",
			cwd:  bareCheckout, argv: []string{"npm", "run", "dev"},
			want: Resolution{Group: "my-repo", Name: "dev"},
		},
		{
			desc: "a directory inside the checkout still resolves to it",
			cwd:  bareCheckout, argv: []string{"./dev.sh"},
			want: Resolution{Group: "my-repo", Name: "dev.sh"},
		},
		{
			desc: "a manifest beats the directory name",
			cwd:  configured, argv: []string{"uv", "run", "uvicorn", "app:app"},
			want: Resolution{
				Group: "option-berth", Name: "api",
				ConfigPath: filepath.Join(configured, ".oberth.yaml"),
			},
		},
		{
			desc: "a service with a cwd does not match from the root",
			cwd:  configured, argv: []string{"npm", "run", "dev"},
			want: Resolution{
				Group: "option-berth", Name: "dev",
				ConfigPath: filepath.Join(configured, ".oberth.yaml"),
			},
		},
		{
			desc: "the same command matches that service from its own cwd",
			cwd:  filepath.Join(configured, "frontend"), argv: []string{"npm", "run", "dev"},
			want: Resolution{
				Group: "option-berth", Name: "web",
				ConfigPath: filepath.Join(configured, ".oberth.yaml"),
			},
		},
		{
			desc: "no manifest and no checkout falls back to the directory",
			cwd:  plainDir, argv: []string{"python3", "-m", "http.server"},
			want: Resolution{Group: "just-a-dir", Name: "http.server"},
		},
		{
			desc: "flags beat everything the tree says",
			cwd:  configured, argv: []string{"uv", "run", "uvicorn", "app:app"},
			group: "itest", name: "web",
			want: Resolution{
				Group: "itest", Name: "web",
				ConfigPath: filepath.Join(configured, ".oberth.yaml"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := Resolve(tc.cwd, tc.argv, tc.group, tc.name); got != tc.want {
				t.Fatalf("Resolve = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A worktree kept beside its main checkout is a distinct group named
// `<project>@<worktree>`, and the config that decided the name is the
// worktree's own copy — not the main checkout's.
func TestResolveWorktreeGroupName(t *testing.T) {
	main := project(t, "option-berth", `
.git/worktrees/feature/
`)
	worktree := project(t, "feature", `
.git
  gitdir: `+filepath.Join(main, ".git", "worktrees", "feature")+`
`)
	if got := Resolve(worktree, []string{"npm", "run", "dev"}, "", "").Group; got != "option-berth@feature" {
		t.Fatalf("worktree group = %q, want option-berth@feature", got)
	}
}

// TestResolveThroughASymlinkedCwd is the macOS $TMPDIR case (/var is a symlink
// to /private/var): a run started in a directory reached through a symlink has
// to land in the same group as one started in the directory itself, and that
// group comes from the `.oberth.yaml`, not from the name of the link.
func TestResolveThroughASymlinkedCwd(t *testing.T) {
	real := project(t, "proj", `
.git
.oberth.yaml
  name: my-app
`)
	viaLink := link(t, real)

	direct := Resolve(real, []string{"npm", "run", "dev"}, "", "")
	if direct.Group != "my-app" {
		t.Fatalf("direct group = %q, want my-app", direct.Group)
	}
	if via := Resolve(viaLink, []string{"npm", "run", "dev"}, "", ""); via != direct {
		t.Fatalf("resolution through the symlink = %+v, want %+v", via, direct)
	}
}

// TestResolveNamesTheRealDirectoryNotTheLink: with no manifest and no checkout
// the group is the directory name, and that is the directory the link points
// at, not the link.
func TestResolveNamesTheRealDirectoryNotTheLink(t *testing.T) {
	viaLink := link(t, project(t, "checkout", ""))
	if got := Resolve(viaLink, []string{"npm", "run", "dev"}, "", "").Group; got != "checkout" {
		t.Fatalf("group = %q, want checkout", got)
	}
}

// TestResolveReportsTheDecidingConfig: the caller shows this path to a person,
// so it has to be the manifest that actually answered — not merely one that
// exists somewhere above the directory.
func TestResolveReportsTheDecidingConfig(t *testing.T) {
	root := project(t, "shop", `
.git
.oberth.yaml
  name: shop
nested/
`)
	got := Resolve(filepath.Join(root, "nested"), []string{"npm", "run", "dev"}, "", "")
	if want := filepath.Join(root, ".oberth.yaml"); got.ConfigPath != want {
		t.Fatalf("config path = %q, want %q", got.ConfigPath, want)
	}

	// A directory with no manifest above it reports none, rather than naming a
	// path the user could not inspect.
	if got := Resolve(project(t, "loose", ""), []string{"npm", "run", "dev"}, "", ""); got.ConfigPath != "" {
		t.Fatalf("config path = %q, want none", got.ConfigPath)
	}
}
