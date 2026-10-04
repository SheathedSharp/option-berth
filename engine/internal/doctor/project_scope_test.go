package doctor

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectConfigFindsNestedAncestor(t *testing.T) {
	env := fakeEnv(t)
	root := env.Home
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	manifest := write(t, filepath.Join(root, "apps", "api", "oberth.yaml"), "name: nested\n")
	env.Project = filepath.Join(root, "apps", "api", "src")
	write(t, filepath.Join(env.Project, "keep"), "")
	got := run(t, env, checkProjectConfig)
	wantStatus(t, got, StatusOK)
	if !strings.Contains(got.Detail, filepath.Base(manifest)) || !strings.Contains(got.Summary, "nested") {
		t.Fatal(got)
	}
}
