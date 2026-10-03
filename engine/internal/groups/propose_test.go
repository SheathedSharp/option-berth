package groups

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

// TestProposeReportsWhatIsListening: a proposal declares no services at all. It
// reports the listeners inside the checkout as candidates, which Marshal writes
// as a comment — the file is a person's to fill in, and option-berth will not
// infer a start command from a running process.
func TestProposeReportsWhatIsListening(t *testing.T) {
	f := newFixture(t)
	index := NewIndex()
	index.AddComposeProject("option-berth", f.composeIn)

	pp := []ports.ListeningPort{
		{Port: 8124, PID: 2, Process: "python3", Command: "python3 -m http.server 8124", Cwd: f.repoSub},
		{Port: 8123, PID: 1, Process: "python3", Command: "python3 -m http.server 8123", Cwd: f.repo},
		// The same listener on a second bind address must not repeat.
		{Port: 8123, PID: 1, Process: "python3", Command: "python3 -m http.server 8123", Cwd: f.repo, BindAddress: "::1"},
		// A Compose container started from inside the repo.
		{Port: 5432, PID: 9, Process: "com.docke", Type: ports.PortTypeDocker,
			DockerContainer: "option-berth-db-1", DockerImage: "postgres:17",
			DockerComposeService: "db", DockerComposeProject: "option-berth"},
		// All listeners whose cwd is inside the worktree are candidates; a process
		// type is evidence, not a project ownership filter.
		{Port: 7000, PID: 3, Process: "Figma", Cwd: f.repo},
		{Port: 22, PID: 4, Process: "sshd", Type: ports.PortTypeUser, Cwd: f.repo},
		{Port: 9999, PID: 5, Process: "node", Cwd: f.composeOut},
	}

	cfg := Propose(f.repo, pp, index)

	if cfg.Name != "option-berth" {
		t.Errorf("name = %q, want the repo directory name", cfg.Name)
	}
	if len(cfg.Services) != 0 {
		t.Fatalf("services = %+v, want none: a proposal declares nothing", cfg.Services)
	}
	if len(cfg.Candidates) != 5 {
		t.Fatalf("candidates = %+v, want all five listeners inside the worktree", cfg.Candidates)
	}
	want := []struct {
		port int
		cwd  string
	}{{22, ""}, {5432, "deploy"}, {7000, ""}, {8123, ""}, {8124, "backend"}}
	for i, want := range want {
		got := cfg.Candidates[i]
		if got.Port != want.port || got.Cwd != want.cwd {
			t.Errorf("candidate[%d] = %+v, want port %d cwd %q", i, got, want.port, want.cwd)
		}
	}

	// What init writes must load back cleanly, and must tell the reader what was
	// there without declaring any of it.
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.repo, ConfigName)
	writeFile(t, path, string(data))
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("the proposed config does not validate: %v", err)
	}
	if loaded.Name != cfg.Name || len(loaded.Services) != 0 {
		t.Errorf("round trip = %+v, want the name and no services", loaded)
	}
	if !strings.HasPrefix(string(data), "# "+ConfigName) {
		t.Errorf("generated file has no header:\n%s", data)
	}
	for _, want := range []string{"services: []", "5432", "db", "8124", "backend"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("generated file does not mention %q:\n%s", want, data)
		}
	}
}

// TestMarshalWritesDeclaredServices: a list the caller put on the config — a
// curated one, from the app — is still written as
// YAML, at the two-space indent a hand-written file uses.
func TestMarshalWritesDeclaredServices(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "api", Cmd: "pnpm run dev", Port: 8000, Cwd: "backend"},
	}}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"services:\n  - name: api\n", "    cmd: pnpm run dev\n", "    cwd: backend\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("generated file is missing %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "services: []") {
		t.Errorf("a declared list was written as empty:\n%s", data)
	}
}

// TestMarshalQuotesANameYAMLWouldMisread: a project directory can be called
// anything the validator allows, including words YAML would rather read as
// something else. The name must survive the round trip as a string.
func TestMarshalQuotesANameYAMLWouldMisread(t *testing.T) {
	for _, name := range []string{"true", "null", "on", "123"} {
		cfg := &Config{Name: name}
		data, err := Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		path := filepath.Join(dir, ConfigName)
		writeFile(t, path, string(data))
		loaded, err := Load(path)
		if err != nil {
			t.Fatalf("name %q does not validate: %v\n%s", name, err, data)
		}
		if loaded.Name != name {
			t.Errorf("name %q came back as %q", name, loaded.Name)
		}
	}
}

// TestProposeNamesWorktrees: a linked worktree gets the qualified name, and
// every listener inside it is reported once. There is no name uniquing any more:
// nothing numbered becomes a service, so two node processes are simply two
// lines in a comment.
func TestProposeNamesWorktrees(t *testing.T) {
	f := newFixture(t)
	pp := []ports.ListeningPort{
		{Port: 3000, PID: 1, Process: "node", Cwd: f.worktree},
		{Port: 3001, PID: 2, Process: "node", Cwd: f.worktree},
		{Port: 3002, PID: 3, Process: "!!!", Cwd: f.worktree},
	}
	cfg := Propose(f.worktree, pp, nil)
	if cfg.Name != "option-berth@feature-x" {
		t.Errorf("name = %q, want the qualified worktree name", cfg.Name)
	}
	if len(cfg.Candidates) != 3 {
		t.Fatalf("candidates = %+v, want all three listeners inside the worktree", cfg.Candidates)
	}
	for i, want := range []int{3000, 3001, 3002} {
		if got := cfg.Candidates[i].Port; got != want {
			t.Errorf("candidate %d = %d, want %d", i, got, want)
		}
	}
}

func TestProposeEmptyRepo(t *testing.T) {
	f := newFixture(t)
	cfg := Propose(f.repo, nil, nil)
	if len(cfg.Services) != 0 || len(cfg.Candidates) != 0 {
		t.Fatalf("cfg = %+v, want nothing proposed and nothing seen", cfg)
	}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Nothing was listening inside this project") {
		t.Errorf("an empty proposal should say so:\n%s", data)
	}
	path := filepath.Join(f.repo, ConfigName)
	writeFile(t, path, string(data))
	if _, err := Load(path); err != nil {
		t.Fatalf("an empty proposal does not validate: %v", err)
	}
}

// TestProposeUsesProjectRootWhenTheComposeDirIsGone is the daemon's case. A
// published state.Port carries no Compose working directory — the resolver
// consumes it before publishing — so a report built from a snapshot has only
// project_root to go on. Without the fallback every container silently drops out
// of a `groups.init` served by the daemon.
func TestProposeUsesProjectRootWhenTheComposeDirIsGone(t *testing.T) {
	f := newFixture(t)

	pp := []ports.ListeningPort{
		{Port: 5432, PID: 9, Process: "com.docke", Type: ports.PortTypeDocker,
			DockerContainer: "option-berth-db-1", DockerImage: "postgres:17",
			DockerComposeService: "db", DockerComposeProject: "option-berth",
			ProjectRoot: f.repo},
		// Attributed to a checkout that is not the one being reported on.
		{Port: 6379, PID: 10, Process: "com.docke", Type: ports.PortTypeDocker,
			DockerContainer: "other-cache-1", DockerComposeService: "cache",
			DockerComposeProject: "other", ProjectRoot: f.composeOut},
	}

	// No index at all: the compose arm of portDir cannot answer.
	cfg := Propose(f.repo, pp, nil)

	if len(cfg.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want only the container inside the repo", cfg.Candidates)
	}
	if got := cfg.Candidates[0]; got.Port != 5432 || got.Process != "db" || got.Cwd != "" {
		t.Errorf("candidate = %+v, want db on 5432 at the repo root", got)
	}
}
