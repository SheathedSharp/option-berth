package git

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolvePathMatchesGitRelativeKeys(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		"sub/deep.txt",
		filepath.Join("sub", "deep.txt"),
		filepath.Join(root, "sub", "deep.txt"),
	} {
		got, ok := resolvePath(root, path)
		if !ok || got != "sub/deep.txt" {
			t.Fatalf("path=%q resolved=(%q,%v)", path, got, ok)
		}
	}
	for _, path := range []string{
		filepath.Join("..", "outside.txt"),
		filepath.Join(root+"-sibling", "outside.txt"),
	} {
		if got, ok := resolvePath(root, path); ok {
			t.Fatalf("outside path %q accepted as %q", path, got)
		}
	}
	if runtime.GOOS != "windows" {
		if got, ok := resolvePath(root, `literal\name.txt`); !ok || got != `literal\name.txt` {
			t.Fatalf("Unix filename was treated as a Windows path: %q,%v", got, ok)
		}
	}
}
