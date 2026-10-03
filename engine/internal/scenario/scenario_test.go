package scenario

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// release cleans up anything a fixture holds open.
func release(t *testing.T, b Built) {
	t.Helper()
	if b.Cleanup != nil {
		b.Cleanup()
	}
}

// isGit reports whether dir is inside a git work tree.
func isGit(dir string) bool {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// loadManifest parses the fixture's oberth.yaml through the engine's own loader.
func loadManifest(t *testing.T, path string) *groups.Config {
	t.Helper()
	cfg, err := groups.Load(path)
	if err != nil {
		t.Fatalf("loading %s: %v", path, err)
	}
	return cfg
}

func service(cfg *groups.Config, name string) *groups.Service {
	for i := range cfg.Services {
		if cfg.Services[i].Name == name {
			return &cfg.Services[i]
		}
	}
	return nil
}

func TestAPIServerAndWorker(t *testing.T) {
	b, err := APIServerAndWorker(t.TempDir())
	if err != nil {
		t.Fatalf("APIServerAndWorker: %v", err)
	}
	t.Cleanup(func() { release(t, b) })

	if !isGit(b.Root) {
		t.Errorf("project root %s is not a git checkout", b.Root)
	}
	cfg := loadManifest(t, b.Manifest)
	if cfg.Name != "api-worker" {
		t.Errorf("name = %q, want api-worker", cfg.Name)
	}
	if len(cfg.Services) != 2 {
		t.Fatalf("services = %d, want 2", len(cfg.Services))
	}
	api := service(cfg, "api")
	if api == nil {
		t.Fatal("no api service")
	}
	if !api.HasPort() || api.Port != b.Port {
		t.Errorf("api port = %d (has %v), want %d", api.Port, api.HasPort(), b.Port)
	}
	if worker := service(cfg, "worker"); worker == nil {
		t.Error("no worker service")
	} else if worker.HasPort() {
		t.Error("worker must declare no port: it runs with none (liveness case)")
	}
	if api := service(cfg, "api"); api == nil {
		t.Error("no api service")
	} else if len(api.DependsOn) != 1 || api.DependsOn[0] != "worker" {
		t.Errorf("api depends_on = %v, want [worker]", api.DependsOn)
	}
	if _, err := os.Stat(filepath.Join(b.Root, "README.scenario.md")); err != nil {
		t.Errorf("scenario note missing: %v", err)
	}
}

func TestStartupFailure(t *testing.T) {
	b, err := StartupFailure(t.TempDir())
	if err != nil {
		t.Fatalf("StartupFailure: %v", err)
	}
	t.Cleanup(func() { release(t, b) })
	if b.Cleanup == nil {
		t.Error("StartupFailure must return a cleanup: it holds the clash port")
	}

	cfg := loadManifest(t, b.Manifest)
	if cfg.Name != "startup-failure" {
		t.Errorf("name = %q, want startup-failure", cfg.Name)
	}
	if broken := service(cfg, "broken"); broken == nil {
		t.Error("no broken service")
	} else if broken.HasPort() {
		t.Error("broken must declare no port: it fails before binding one")
	}
	if clash := service(cfg, "clash"); clash == nil {
		t.Error("no clash service")
	} else if clash.Port != b.Port {
		t.Errorf("clash port = %d, want the fixture-held %d", clash.Port, b.Port)
	}
}

func TestMachineDependency(t *testing.T) {
	b, err := MachineDependency(t.TempDir(), "mysql")
	if err != nil {
		t.Fatalf("MachineDependency: %v", err)
	}

	cfg := loadManifest(t, b.Manifest)
	if cfg.Name != "machine-dep" {
		t.Errorf("name = %q, want machine-dep", cfg.Name)
	}
	// The worker makes the project startable (and so registerable); it is not
	// the subject — the machine ref is.
	if len(cfg.Services) != 1 || service(cfg, "worker") == nil {
		t.Errorf("services = %v, want exactly a worker", cfg.Services)
	}
	if len(cfg.Machine) != 1 {
		t.Fatalf("machine = %d, want 1", len(cfg.Machine))
	}
	if cfg.Machine[0].Name != "mysql" {
		t.Errorf("machine ref name = %q, want mysql", cfg.Machine[0].Name)
	}
	if cfg.Machine[0].Port <= 0 {
		t.Errorf("machine ref port = %d, want a real port", cfg.Machine[0].Port)
	}
}

func TestDualWorktree(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	wtDir := filepath.Join(root, "wt")

	main, wt, err := DualWorktree(mainDir, wtDir)
	if err != nil {
		t.Fatalf("DualWorktree: %v", err)
	}

	// The main checkout is a real repo, the linked checkout a worktree of it.
	if !isGit(main.Root) {
		t.Errorf("main root %s is not a git checkout", main.Root)
	}
	out, err := exec.Command("git", "-C", main.Root, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if got := strings.Count(string(out), "\n"); got < 2 {
		t.Errorf("worktree list has %d entries, want >= 2:\n%s", got, out)
	}

	if mainCfg := loadManifest(t, main.Manifest); mainCfg.Name != "dual-main" {
		t.Errorf("main name = %q, want dual-main", mainCfg.Name)
	}
	if wtCfg := loadManifest(t, wt.Manifest); wtCfg.Name != "dual-main@wt" {
		t.Errorf("worktree name = %q, want dual-main@wt", wtCfg.Name)
	}
	if wt.Port == main.Port {
		t.Errorf("main and worktree picked the same port %d; they must differ", main.Port)
	}
}
