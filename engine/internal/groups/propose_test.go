package groups

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// proposeFileHeader is the exact measured Marshal header, including the editor
// schema modeline. The production constant embeds a backtick around `oberth init`.
var proposeFileHeader = `# yaml-language-server: $schema=https://raw.githubusercontent.com/SheathedSharp/option-berth/main/engine/docs/schema/manifest.schema.json
# oberth.yaml — option-berth worktree service configuration, written by ` + "`oberth init`" + `.
# Edit and commit it: option-berth uses it to name this worktree and to know
# which services belong to it. https://github.com/sheathedsharp/option-berth/engine
#
# The services list below is yours to write. option-berth starts and stops exactly
# what it declares, and it does not infer a service from a running process: a
# process's command line is a snapshot of one run — the arguments a shell, an IDE
# or a supervisor happened to pass — not a statement of how to bring the service
# back. A file full of entries guessed that way looks finished and starts nothing.
`

// starterComment is the measured copy/machine/workspace comment block that
// Marshal writes under the name and services.
const starterComment = `
# A service, to copy:
#
#   - name: api
#     prepare: pnpm install          # optional: rebuild an artifact first
#     prepare_inputs: [package.json, pnpm-lock.yaml]  # skip prepare while these are unchanged
#     cmd: pnpm run dev              # what brings it back up
#     port: 8000                     # optional: the port it binds
#     cwd: .                         # optional: where to run it, relative to this file
#     env_file: ../shared/dev.env    # optional: dotenv file read at start, never written
#     health: /healthz               # optional: HTTP path; or {tcp: 5432} / {command: pg_isready}
#     env:                           # optional, added to its environment
#       NODE_ENV: development
#
# A service the machine owns (a database started at login, an already-running
# redis) is not a service of this project: it goes in the file's own machine:
# list as a reference — option-berth never starts or stops one, so it carries no
# cmd:
#   machine:
#     - name: mysql
#       port: 3306
#
# A service in another checkout is addressed the same way, read-only, through
# workspace: — ${backend.url} then follows that checkout's own port, including
# an assigned one, and nothing here starts or stops it:
#   workspace:
#     - name: backend
#       worktree: ../backend
#       service: api
#
`

var listeningCommentIntro = `# Listening inside this project when this file was written. This is context to
# write from, not a declaration — nothing here was added to the list for you.
# The command line is how the process was started *this time*: it is there to
# tell two listeners apart, not to be copied into ` + "`cmd:`" + `.
#
`

const idleListeningComment = `# Nothing was listening inside this project when this file was written.
`

func requireProposedConfig(t *testing.T, got *Config, name, dir, path string, candidates []Candidate) {
	t.Helper()
	want := &Config{Name: name, Dir: dir, Path: path, Candidates: candidates}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cfg = %#v, want %#v", got, want)
	}
}

func requireLoadedProposal(t *testing.T, got *Config, name, path string) {
	t.Helper()
	want := &Config{Name: name, Dir: filepath.Dir(path), Path: path, Services: []Service{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("loaded = %#v, want %#v", got, want)
	}
}

func requireMarshal(t *testing.T, got []byte, body string) {
	t.Helper()
	want := proposeFileHeader + body
	if string(got) != want {
		t.Errorf("marshal =\n%s\nwant\n%s", got, want)
	}
}

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
	requireProposedConfig(t, cfg, "option-berth", f.repo, filepath.Join(f.repo, ConfigName), []Candidate{
		{Port: 22, Process: "sshd", PID: 4},
		{Port: 5432, Process: "db", Cwd: "deploy", PID: 9},
		{Port: 7000, Process: "Figma", PID: 3},
		{Port: 8123, Process: "option-berth/http.server", PID: 1, Command: "python3 -m http.server 8123"},
		{Port: 8124, Process: "backend/http.server", Cwd: "backend", PID: 2, Command: "python3 -m http.server 8124"},
	})

	// What init writes must load back cleanly, and must tell the reader what was
	// there without declaring any of it.
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	requireMarshal(t, data, "name: option-berth\nservices: []\n"+starterComment+listeningCommentIntro+`#     22  sshd             .  pid 4
#   5432  db               deploy  pid 9
#   7000  Figma            .  pid 3
#   8123  option-berth/http.server .  pid 1
#       python3 -m http.server 8123
#   8124  backend/http.server backend  pid 2
#       python3 -m http.server 8124
`)
	path := filepath.Join(f.repo, ConfigName)
	writeFile(t, path, string(data))
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("the proposed config does not validate: %v", err)
	}
	requireLoadedProposal(t, loaded, "option-berth", path)
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
	requireMarshal(t, data, `name: demo
services:
  - name: api
    cmd: pnpm run dev
    cwd: backend
    port: 8000
`+starterComment+idleListeningComment)
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
		requireMarshal(t, data, "name: \""+name+"\"\nservices: []\n"+starterComment+idleListeningComment)
		dir := t.TempDir()
		path := filepath.Join(dir, ConfigName)
		writeFile(t, path, string(data))
		loaded, err := Load(path)
		if err != nil {
			t.Fatalf("name %q does not validate: %v\n%s", name, err, data)
		}
		requireLoadedProposal(t, loaded, name, path)
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
	requireProposedConfig(t, cfg, "option-berth@feature-x", f.worktree, filepath.Join(f.worktree, ConfigName), []Candidate{
		{Port: 3000, Process: "node", PID: 1},
		{Port: 3001, Process: "node", PID: 2},
		{Port: 3002, Process: "!!!", PID: 3},
	})
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	requireMarshal(t, data, "name: option-berth@feature-x\nservices: []\n"+starterComment+listeningCommentIntro+`#   3000  node             .  pid 1
#   3001  node             .  pid 2
#   3002  !!!              .  pid 3
`)
}

func TestProposeEmptyRepo(t *testing.T) {
	f := newFixture(t)
	cfg := Propose(f.repo, nil, nil)
	requireProposedConfig(t, cfg, "option-berth", f.repo, filepath.Join(f.repo, ConfigName), nil)
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	requireMarshal(t, data, "name: option-berth\nservices: []\n"+starterComment+idleListeningComment)
	path := filepath.Join(f.repo, ConfigName)
	writeFile(t, path, string(data))
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("an empty proposal does not validate: %v", err)
	}
	requireLoadedProposal(t, loaded, "option-berth", path)
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
	requireProposedConfig(t, cfg, "option-berth", f.repo, filepath.Join(f.repo, ConfigName), []Candidate{
		{Port: 5432, Process: "db", PID: 9},
	})
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	requireMarshal(t, data, "name: option-berth\nservices: []\n"+starterComment+listeningCommentIntro+`#   5432  db               .  pid 9
`)
}

// An unknown observation must never render as an idle machine. This is the
// comment a person writes the service list from; "nothing was listening" when
// the scan never read the table is a fact the file does not have.
func TestProposeSaysListeningIsUnknownWhenTheScanFailed(t *testing.T) {
	f := newFixture(t)
	cfg := Propose(f.repo, nil, nil)
	cfg.Observation = &ListenerObservation{
		State:  "blind",
		Code:   "observation_blind",
		Detail: "bound 127.0.0.1:51234 but the listener table did not show it\ntrailing line",
		Hint:   "retry outside the sandbox\nor run `oberth doctor`",
	}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	requireMarshal(t, data, "name: option-berth\nservices: []\n"+starterComment+`# Listening inside this project is unknown: the scan could not read
# this machine's listener table when this file was written.
#   observation_blind: bound 127.0.0.1:51234 but the listener table did not show it
#   next: retry outside the sandbox
`)
	// The comment is the only change: the file still loads.
	path := filepath.Join(f.repo, ConfigName)
	writeFile(t, path, string(data))
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("the degraded starter does not validate: %v", err)
	}
	requireLoadedProposal(t, loaded, "option-berth", path)
}

// A partial pass read some rows, so its candidates are facts and the file says
// "nothing was listening" only when there is genuinely nothing to report.
func TestProposePartialObservationKeepsTheIdleSentence(t *testing.T) {
	f := newFixture(t)
	cfg := Propose(f.repo, nil, nil)
	cfg.Observation = &ListenerObservation{State: ObservationPartial, Code: "observation_incomplete"}
	if cfg.Observation.Unknown() {
		t.Fatal("a partial observation still read the table; it is not unknown")
	}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	requireMarshal(t, data, "name: option-berth\nservices: []\n"+starterComment+idleListeningComment)
}
