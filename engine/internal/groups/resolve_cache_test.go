package groups

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestProjectCheckoutCachePreservesFallbacks(t *testing.T) {
	base := tempTree(t)
	outside := mkdir(t, base, "outside")
	first := mkdir(t, base, "first")
	second := mkdir(t, base, "second")
	mkdir(t, first, ".git")
	mkdir(t, second, ".git")
	linked := mkdir(t, base, "feature")
	writeFile(t, filepath.Join(linked, ".git"),
		"gitdir: "+filepath.Join(first, ".git", "worktrees", "feature")+"\n")

	index := NewIndex()
	index.AddComposeProject("first-compose", first)
	index.AddComposeProject("second-compose", second)
	index.AddComposeProject("outside-compose", outside)
	index.AddComposeProject("linked-compose", linked)
	port := func(cwd, project string) state.Port {
		p := state.Port{Cwd: cwd}
		if project != "" {
			p.Docker = &state.Docker{ComposeProject: project}
		}
		return p
	}
	type step struct {
		port state.Port
		root string
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{"native miss before compose", []step{
			{port(outside, ""), ""},
			{port(outside, "first-compose"), first},
		}},
		{"repeated compose listener", []step{
			{port(outside, "first-compose"), first},
			{port(outside, "first-compose"), first},
		}},
		{"shared cwd different compose projects", []step{
			{port(outside, "first-compose"), first},
			{port(outside, "second-compose"), second},
			{port(outside, "first-compose"), first},
		}},
		{"negative compose before positive compose", []step{
			{port(outside, "outside-compose"), ""},
			{port(outside, "second-compose"), second},
		}},
		{"cwd retains precedence", []step{
			{port(first, "second-compose"), first},
			{port(first, "second-compose"), first},
		}},
		{"empty cwd uses compose", []step{
			{port("", "first-compose"), first},
			{port("", "second-compose"), second},
		}},
		{"linked worktree survives warm cache", []step{
			{port(outside, "linked-compose"), linked},
			{port(outside, "linked-compose"), linked},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			round := newResolutionPass(index)
			// Repeat the batch to exercise both cold and warm lookups. The
			// expected checkout comes from directory facts, not the resolver.
			for pass := 0; pass < 2; pass++ {
				for i, step := range tt.steps {
					want, wantOK := Locate(step.root)
					got, ok := round.projectCheckout(&step.port)
					if ok != wantOK || got != want {
						t.Fatalf("pass=%d row=%d cwd=%q compose=%+v: got (%+v, %v), want (%+v, %v)",
							pass, i, step.port.Cwd, step.port.Docker, got, ok, want, wantOK)
					}
				}
			}
		})
	}
}

func TestProjectCheckoutCacheIsScanScoped(t *testing.T) {
	base := tempTree(t)
	outside := mkdir(t, base, "outside")
	repo := mkdir(t, base, "repo")
	gitDir := mkdir(t, repo, ".git")
	index := NewIndex()
	index.AddComposeProject("compose", repo)
	p := state.Port{Cwd: outside, Docker: &state.Docker{ComposeProject: "compose"}}
	round := newResolutionPass(index)
	want, found := round.projectCheckout(&p)
	if !found || want.Root != repo {
		t.Fatalf("initial checkout = (%+v, %v)", want, found)
	}
	if err := os.Remove(gitDir); err != nil {
		t.Fatal(err)
	}
	if got, ok := round.projectCheckout(&p); !ok || got != want {
		t.Fatalf("same scan discarded cached facts: (%+v, %v)", got, ok)
	}
	if got, ok := newResolutionPass(index).projectCheckout(&p); ok {
		t.Fatalf("new scan reused removed checkout: %+v", got)
	}
	mkdir(t, repo, ".git")
	if got, ok := newResolutionPass(index).projectCheckout(&p); !ok || got != want {
		t.Fatalf("new scan missed restored checkout: (%+v, %v)", got, ok)
	}
}
