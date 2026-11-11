package groups

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

type fakeRun struct{ group, name string }
type fakeRuns map[int]fakeRun

func (f fakeRuns) Run(p state.Port) (state.Run, bool) {
	r, ok := f[p.Port]
	return state.Run{Group: r.group, Name: r.name}, ok
}

// resolveFixture builds a repo, a linked worktree, a Compose project inside the
// repo and a Compose project outside any repo, and returns the paths.
type fixture struct {
	repo       string // a clone with .git
	repoSub    string // a directory inside the clone
	worktree   string // a linked worktree of the clone
	composeIn  string // compose working_dir inside the clone
	composeOut string // compose working_dir outside any repo
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := tempTree(t)
	repo := mkdir(t, base, "option-berth")
	mkdir(t, repo, ".git")
	f := fixture{
		repo:       repo,
		repoSub:    mkdir(t, repo, "backend"),
		worktree:   mkdir(t, base, "wt", "feature-x"),
		composeIn:  mkdir(t, repo, "deploy"),
		composeOut: mkdir(t, base, "loose"),
	}
	writeFile(t, filepath.Join(f.worktree, ".git"),
		"gitdir: "+filepath.Join(repo, ".git", "worktrees", "feature-x")+"\n")
	return f
}

func nativePort(port int, cwd string) state.Port {
	return state.Port{Port: port, BindAddress: "127.0.0.1", Cwd: cwd, Process: "python3"}
}

func composePort(port int, project, service string) state.Port {
	return state.Port{
		Port: port, BindAddress: "0.0.0.0", Process: "com.docke",
		Type:   state.TypeDocker,
		Docker: &state.Docker{Container: project + "-" + service + "-1", ComposeProject: project, ComposeService: service},
	}
}

type resolvedWant struct {
	group  string
	source state.GroupSource
	root   string
}

func requireResolved(t *testing.T, in state.Port, runs Registry, index *Index, want resolvedWant) {
	t.Helper()
	before := in
	out := Resolve([]state.Port{in}, runs, index)
	if !reflect.DeepEqual(in, before) {
		t.Errorf("Resolve mutated its input: %+v", in)
	}
	if len(out) != 1 {
		t.Fatalf("Resolve returned %d ports, want 1", len(out))
	}
	got := out[0]
	if got.Port != in.Port || got.BindAddress != in.BindAddress || got.Cwd != in.Cwd || got.Process != in.Process || got.Type != in.Type || !reflect.DeepEqual(got.Docker, in.Docker) {
		t.Errorf("Resolve changed identity fields: got %+v, want the input %+v", got, in)
	}
	if got.Name != nil || got.Run != nil {
		t.Errorf("Resolve stamped Name=%v Run=%v, want both unset", got.Name, got.Run)
	}
	if want.group == "" && want.source == "" && want.root == "" {
		if got.Group != nil || got.GroupSource != nil || got.ProjectRoot != nil {
			t.Errorf("ungrouped port has group=%q source=%v root=%v, want all nil", deref(got.Group), got.GroupSource, got.ProjectRoot)
		}
		return
	}
	if got.Group == nil || *got.Group != want.group {
		t.Errorf("group = %v, want %q", got.Group, want.group)
	}
	if got.GroupSource == nil || *got.GroupSource != want.source {
		t.Errorf("group_source = %v, want %s", got.GroupSource, want.source)
	}
	if want.root == "" {
		if got.ProjectRoot != nil {
			t.Errorf("project_root = %q, want nil", deref(got.ProjectRoot))
		}
		return
	}
	if got.ProjectRoot == nil || *got.ProjectRoot != want.root {
		t.Errorf("project_root = %v, want %q", got.ProjectRoot, want.root)
	}
}

func TestResolvePrecedence(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.repo, ConfigName),
		"name: option-berth-cfg\nservices:\n  - name: api\n    port: 8000\nports: [9229]\n")

	index := NewIndex()
	index.Observe(f.repoSub)
	index.AddComposeProject("deploy-stack", f.composeIn)
	index.AddComposeProject("loose-stack", f.composeOut)
	runs := fakeRuns{7100: {group: "started", name: "web"}, 7200: {group: "", name: "tagged-only"}}

	cases := []struct {
		name string
		port state.Port
		want resolvedWant
	}{
		{
			name: "a oberth start run beats the config",
			port: nativePort(7100, f.repoSub),
			want: resolvedWant{group: "started", source: state.SourceStart, root: f.repo},
		},
		{
			name: "a run without a group falls through to the config",
			port: nativePort(7200, f.repoSub),
			want: resolvedWant{group: "option-berth-cfg", source: state.SourceFile, root: f.repo},
		},
		{
			name: "a config service port claims the port",
			port: nativePort(8000, f.repoSub),
			want: resolvedWant{group: "option-berth-cfg", source: state.SourceFile, root: f.repo},
		},
		{
			name: "a config ports entry claims the port",
			port: nativePort(9229, f.repo),
			want: resolvedWant{group: "option-berth-cfg", source: state.SourceFile, root: f.repo},
		},
		{
			name: "a compose project inside the repo merges into it",
			port: composePort(5432, "deploy-stack", "db"),
			want: resolvedWant{group: "option-berth-cfg", source: state.SourceFile, root: f.repo},
		},
		{
			name: "a compose project outside any repo keeps its own name",
			port: composePort(5433, "loose-stack", "db"),
			want: resolvedWant{group: "loose-stack", source: state.SourceAuto},
		},
		{
			name: "a compose project with no known directory keeps its name",
			port: composePort(5434, "unknown-stack", "db"),
			want: resolvedWant{group: "unknown-stack", source: state.SourceAuto},
		},
		{
			// The project half is the main checkout's name, and the main
			// checkout's oberth.yaml names it. The worktree itself has no
			// manifest, so the source stays auto.
			name: "a worktree is its own group",
			port: nativePort(8100, f.worktree),
			want: resolvedWant{group: "option-berth-cfg@feature-x", source: state.SourceAuto, root: f.worktree},
		},
		{
			name: "a process with no cwd and no docker is ungrouped",
			port: nativePort(8200, ""),
		},
		{
			name: "a process outside any repo is ungrouped",
			port: nativePort(8300, f.composeOut),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			requireResolved(t, c.port, runs, index, c.want)
		})
	}
}

func TestResolveGitRootWithoutConfig(t *testing.T) {
	f := newFixture(t)
	index := NewIndex()
	index.Observe(f.repoSub)
	requireResolved(t, nativePort(8123, f.repoSub), NoRuns{}, index, resolvedWant{
		group:  "option-berth",
		source: state.SourceAuto,
		root:   f.repo,
	})
}

func TestResolveLeavesInputUntouched(t *testing.T) {
	f := newFixture(t)
	in := []state.Port{nativePort(8123, f.repoSub)}
	before := in[0]
	_ = Resolve(in, NoRuns{}, NewIndex())
	if !reflect.DeepEqual(in[0], before) || in[0].Group != nil || in[0].ProjectRoot != nil || in[0].GroupSource != nil {
		t.Fatalf("Resolve mutated its input: %+v", in[0])
	}
}

func TestResolveTolerantOfNilInputs(t *testing.T) {
	requireResolved(t, nativePort(8123, ""), nil, nil, resolvedWant{})
	if got := Resolve(nil, nil, nil); got != nil {
		t.Errorf("Resolve(nil, nil, nil) = %+v, want nil", got)
	}
	empty := Resolve([]state.Port{}, nil, nil)
	if empty == nil || len(empty) != 0 {
		t.Errorf("Resolve(empty, nil, nil) = %#v, want a non-nil empty slice", empty)
	}
}
