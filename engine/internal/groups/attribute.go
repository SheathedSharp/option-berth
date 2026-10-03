package groups

import (
	"os"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// Attribute runs the resolver over a direct scan. It builds an index from what
// the scan saw — every `oberth.yaml` above a process cwd, every Compose
// project's working directory, plus the config in the caller's own working
// directory — resolves the group of every port, writes Group, GroupSource and
// ProjectRoot back onto the scanner rows, and returns the resolved contract
// rows together with the index.
//
// This is the no-daemon path: `oberth status`, `oberth kill` and `oberth init`
// call it. Run ownership is resolved here too, from runs.json and the process
// tree (see DirectRuns): the collection layer reports what the OS shows, and
// this layer decides which worktree, service and run it belongs to. The
// daemon's scanner calls AttributeWith instead, with a long-lived index and
// the `oberth start` registry.
func Attribute(pp []ports.ListeningPort) ([]state.Port, *Index) {
	index := NewIndex()
	if wd, err := os.Getwd(); err == nil {
		index.Observe(wd)
	}
	return AttributeWith(pp, NewDirectRuns(), index)
}

// AttributeWith is Attribute with the run registry and the index supplied by
// the caller, so the daemon can keep one index for its lifetime and feed the
// resolver the registry it knows this tick. A nil index or registry falls back
// to an empty one.
//
// Unlike Attribute it does not index the process's own working directory: the
// daemon's cwd says nothing about the ports it is watching.
func AttributeWith(pp []ports.ListeningPort, runs Registry, index *Index) ([]state.Port, *Index) {
	if index == nil {
		index = NewIndex()
	}
	if runs == nil {
		runs = NoRuns{}
	}

	for i := range pp {
		index.AddComposeProject(pp[i].DockerComposeProject, pp[i].DockerComposeWorkingDir)
	}
	// One pass owns path observations, including shared ancestors. Nothing
	// survives this call: absent manifests and moved checkouts are rechecked.
	pass := &resolutionPass{index: index}
	if len(index.files) > 0 {
		pass = newResolutionPass(index)
	}
	if pass.probed == nil {
		for i := range pp {
			if pp[i].Cwd != "" || index.ComposeDir(pp[i].DockerComposeProject) != "" {
				pass = newResolutionPass(index)
				break
			}
		}
	}
	for dir := range index.files {
		pass.probe(dir)
	}
	for i := range pp {
		pass.observe(pp[i].Cwd)
	}
	for i := range pp {
		pass.observe(index.ComposeDir(pp[i].DockerComposeProject))
	}

	stampRuns(pp, runs)

	// Resolve the captured ownership, not a second read of a live registry.
	// One row must not combine an old run/name with a replacement group.
	resolved := state.FromListeningAll(pp)
	pass.resolve(resolved, PortRuns{})
	stampSessions(resolved, runs)
	for i := range resolved {
		pp[i].ProjectRoot = deref(resolved[i].ProjectRoot)
		pp[i].Group = deref(resolved[i].Group)
		pp[i].GroupSource = ""
		if resolved[i].GroupSource != nil {
			pp[i].GroupSource = string(*resolved[i].GroupSource)
		}
	}

	return resolved, index
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// stampSessions puts the agent session of the owning run onto every port it
// owns, from the same registry and the same ancestry walk that decided the
// run. It runs after Resolve because it stamps the contract rows, not the
// scanner rows: `session` is a daemon concept with no place in `runs.json`, so
// the direct-scan path has nothing to stamp and this is a no-op there.
func stampSessions(resolved []state.Port, runs Registry) {
	reg, ok := runs.(SessionRegistry)
	if !ok {
		return
	}
	for i := range resolved {
		if resolved[i].PID <= 0 || resolved[i].Run == nil {
			continue
		}
		if s, found := reg.Session(resolved[i]); found && s.ID != "" {
			session := s
			resolved[i].Session = &session
		}
	}
}

// stampRuns writes the registry's run attribution onto the scan rows before
// they are converted, so `run`, `display_name` and `group` are all derived
// from the same lookup at the same instant. It runs in the attribution layer
// for both paths: the direct scan brings DirectRuns, the daemon brings its
// in-memory registry, and scanning itself no longer reads runs.json.
func stampRuns(pp []ports.ListeningPort, runs Registry) {
	for i := range pp {
		var run state.Run
		if runs != nil && pp[i].PID > 0 {
			// Read before replacing the old fields: PortRuns intentionally uses
			// the supplied batch itself as its source, unlike a live registry.
			if captured, ok := runs.Run(state.FromListening(pp[i])); ok {
				run = captured
			}
		}
		// A miss revokes prior evidence as well. Republish reuses a listening
		// batch after run removal, without necessarily performing an OS scan.
		pp[i].RunID, pp[i].RunGroup, pp[i].Tag, pp[i].RunRootPID = run.ID, run.Group, run.Name, run.RootPID
	}
}
