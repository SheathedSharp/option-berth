package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// `groups.init` is `oberth init` over the wire: it proposes an `oberth.yaml`
// for a checkout from what is listening in it right now, and — when the caller
// says `write` — puts that file on disk.
//
// The proposal itself is not decided here. groups.Propose turns the listening
// ports into services, and it is the same function `oberth init` uses with its
// own scan, because a proposal that differs between the two paths is a bug the
// user sees as a file they did not mean to write. This handler owns only what
// the daemon can know better than a one-shot CLI: the live snapshot, the
// group it already publishes, and the write.
//
// The daemon has no working directory of the caller's, so unlike the CLI it
// cannot default the root: `root_dir` is required.

func init() {
	// The `groups` capability is already announced by groups_config.go;
	// repeating it here is a no-op that keeps this method's requirements
	// visible next to the method itself.
	RegisterHandler("groups.init", handleGroupsInit)
	RegisterCapability("groups")
}

// initConflicts are the ways a caller can say what a call should write. At most
// one may be set: each decides the whole file, so a call that names two has not
// decided who is writing it, and picking one for the caller would silently
// discard the other.
var initConflicts = []struct {
	both        func(p rpc.GroupsInitParams) bool
	detail, fix string
}{
	{
		both:   func(p rpc.GroupsInitParams) bool { return p.Force && p.Merge },
		detail: "force and merge cannot both be set",
		fix:    "force replaces the file, merge appends to it; pick one",
	},
	{
		both:   func(p rpc.GroupsInitParams) bool { return p.YAML != "" && p.Merge },
		detail: "yaml and merge cannot both be set",
		fix:    "merge appends services to a file that is there; yaml is the file",
	},
	{
		both:   func(p rpc.GroupsInitParams) bool { return p.YAML != "" && len(p.Services) > 0 },
		detail: "yaml and services cannot both be set",
		fix:    "services is a list option-berth renders; yaml is the file it would go in",
	},
}

// checkInitParams refuses a call that names two ways of writing the same file,
// and a curated list with an unnamed entry: both are the caller's mistake,
// reported before anything is read or written.
func checkInitParams(p rpc.GroupsInitParams) error {
	for _, conflict := range initConflicts {
		if conflict.both(p) {
			return rpc.NewError(rpc.CodeInvalidParams, conflict.detail, conflict.fix)
		}
	}
	for _, svc := range p.Services {
		if strings.TrimSpace(svc.Name) == "" {
			return rpc.NewError(rpc.CodeInvalidParams, "services names an empty service",
				`each entry is {"name": "api", "port": 8000}`)
		}
	}
	return nil
}

// initPlan is what one `groups.init` call decided to write, and what it reports
// back whether or not it writes. Building one reads the repository and the
// snapshot; nothing here touches the config file.
type initPlan struct {
	path string
	data []byte
	// declared is how many services this call put in the file, and affected is
	// what the caller reads back: service names, in the order the file lists
	// them, because this method mutates a manifest rather than a port.
	declared int
	affected []string
	proposal state.Group
	// existing is the file this call found already there. Writing over it takes
	// the caller's `force`; a merge appends to it instead.
	existing bool
}

func handleGroupsInit(_ context.Context, req *Request) (any, error) {
	var p rpc.GroupsInitParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if err := checkInitParams(p); err != nil {
		return nil, err
	}

	plan, err := planInit(p, req.Runtime)
	if err != nil {
		return nil, err
	}
	if err := commitInitPlan(plan, p, req); err != nil {
		return nil, err
	}
	return plan.result(), nil
}

// planInit resolves the caller's root and target file, reads the snapshot the
// proposal is built from, and renders the bytes this call would write. It is
// the read half of `groups.init`: nothing here touches the config file, so a
// preview and a write start from exactly the same plan.
func planInit(p rpc.GroupsInitParams, rt *Runtime) (initPlan, error) {
	root, err := initRoot(p.RootDir)
	if err != nil {
		return initPlan{}, err
	}
	// A project that already has a config, under any accepted filename, is
	// merged into or overwritten in place; only a project without one gets a
	// new file.
	path := groups.TargetIn(root)
	// The daemon writes exactly one filename on a client's say-so, and this
	// check is made here too rather than trusted to the join above.
	if err := checkConfigPath(path); err != nil {
		return initPlan{}, err
	}
	snap, err := initSnapshot(rt)
	if err != nil {
		return initPlan{}, err
	}
	return buildInitPlan(p, root, path, snap)
}

// commitInitPlan is the write half: a preview returns without touching disk; a
// write refuses to replace a file the caller did not say to overwrite, then
// puts the planned bytes down and republishes.
func commitInitPlan(plan initPlan, p rpc.GroupsInitParams, req *Request) error {
	if !p.Write {
		// The default is a preview: the caller gets the bytes a write would
		// put on disk and decides, and nothing has been written yet.
		return nil
	}
	// A merge plan never carries `existing`: it appends to the file rather than
	// replacing it, so the overwrite rule is the plain path's alone.
	if plan.existing && !p.Force {
		return rpc.NewError(rpc.CodeInvalidParams, plan.path+" already exists",
			`pass {"force": true} to overwrite it or {"merge": true} to append to it, `+
				"or read it with groups.config.get")
	}
	return plan.write(req)
}

// buildInitPlan turns the call and the snapshot into the file it would write.
// Two paths reach it: a merge into a file that is there, which goes through the
// same node-level edit `groups.config.set` uses so comments and order survive,
// and the plain path, which renders the proposal and reads it back.
func buildInitPlan(p rpc.GroupsInitParams, root, path string, snap state.Snapshot) (initPlan, error) {
	_, statErr := os.Lstat(path)
	existing := statErr == nil
	listening := listeningPorts(snap)

	cfg := proposedConfig(p, root, snap)
	if p.Merge && existing {
		return mergeInitPlan(p, path, cfg, listening)
	}
	plan := initPlan{path: path, existing: existing}

	// The bytes on disk are the caller's when it edited the proposal, and what
	// option-berth rendered when it did not. Either way they are parsed here: a
	// curated list or a hand edit is the caller's word, and this is where a word
	// that would not load is caught — before the file exists, not on the next
	// scan.
	if p.YAML != "" {
		plan.data = []byte(p.YAML)
	} else {
		rendered, err := groups.Marshal(cfg)
		if err != nil {
			return initPlan{}, rpc.NewError(rpc.CodeInternal,
				"rendering "+groups.ConfigName+": "+err.Error(), "check `oberth daemon log`")
		}
		plan.data = rendered
	}
	parsed, err := groups.Parse(path, plan.data)
	if err != nil {
		return initPlan{}, configError(path, err)
	}
	// What was parsed is what this call produced: the name and the services come
	// from the bytes, so an edit that renamed the group or added a service is
	// reported as such rather than as the proposal it started from.
	plan.declare(parsed, listening)
	return plan, nil
}

// mergeInitPlan appends the proposal into a file that is already there, instead
// of refusing it. The append is the node-level edit `groups.config.set` uses,
// so the file's comments and order survive and a name or port the file already
// has is a `conflict`; the bytes are rendered before anything is written, so
// `write: false` previews the merged file exactly.
func mergeInitPlan(p rpc.GroupsInitParams, path string, cfg *groups.Config, listening map[int]bool) (initPlan, error) {
	adds := groups.AddsFrom(cfg)
	// A plain proposal declares nothing — option-berth does not infer services
	// from running processes — so a merge with no service list has nothing to
	// append. Saying so beats reporting an edit that changed nothing.
	if len(adds) == 0 {
		return initPlan{}, rpc.NewError(rpc.CodeInvalidParams, "nothing to merge into "+path,
			"a proposal declares no services; name the ones to append in `services`, "+
				"or read the file with groups.config.get")
	}
	data, merged, err := groups.RenderEdit(path, groups.ConfigEdit{Add: adds})
	if err != nil {
		return initPlan{}, configError(path, err)
	}
	plan := initPlan{
		path:     merged.Path,
		data:     data,
		declared: len(adds),
		affected: serviceNames(cfg),
		proposal: proposalGroup(merged, listening),
	}
	return plan, nil
}

// declare records what the parsed file says, which is what this call reports:
// the names it lists and the group row it would publish.
func (plan *initPlan) declare(cfg *groups.Config, listening map[int]bool) {
	plan.declared = len(cfg.Services)
	plan.affected = serviceNames(cfg)
	plan.proposal = proposalGroup(cfg, listening)
}

// result is the reply, identical for a preview and for a write.
func (plan initPlan) result() rpc.GroupsInitResult {
	return rpc.GroupsInitResult{
		MutationResult: rpc.MutationResult{OK: true, Affected: plan.affected},
		Path:           plan.path,
		YAML:           string(plan.data),
		Proposal:       plan.proposal,
	}
}

// write puts the planned bytes on disk and republishes, so subscribers see the
// group this call created before the caller's own next read.
func (plan initPlan) write(req *Request) error {
	if err := groups.WriteConfigFile(plan.path, plan.data); err != nil {
		return rpc.NewError(rpc.CodeInternal, "writing "+plan.path+": "+err.Error(),
			"check that the daemon may write to "+filepath.Dir(plan.path))
	}
	publishConfig(req, plan.path, plan.proposal.Name, plan.declared)
	return nil
}

// proposedConfig is the proposal a call starts from: what is listening in the
// checkout, what the project's own files declare, and the caller's curated list
// when it sent one.
func proposedConfig(p rpc.GroupsInitParams, root string, snap state.Snapshot) *groups.Config {
	cfg := groups.Propose(root, state.ToListeningAll(snap.Ports), nil)
	// What the project's own files state (a package.json dev script, a compose
	// service) is comment material, exactly like the listeners — the same rule
	// the CLI's `oberth init` follows, so the two paths write the same file.
	cfg.Declared = groups.Detect(root)
	if len(p.Services) == 0 {
		return cfg
	}
	// The caller curated the proposal: names, ports and metadata are theirs, and
	// the cmd the proposal guessed is carried onto an entry that kept the port
	// it was guessed for.
	return groups.Curate(cfg, p.Services)
}

// publishConfig indexes a file the daemon just wrote and republishes, so the
// group reaches subscribers as a groups delta before the caller's own next read
// (the same order `groups.config.set` keeps).
func publishConfig(req *Request, path, group string, services int) {
	scanner := req.Runtime.Scanner
	if err := scanner.LoadConfig(path); err != nil {
		req.Runtime.Logger.Warn("reloading a config after writing it", "path", path, "error", err)
	}
	req.Runtime.Logger.Info("wrote "+groups.ConfigName,
		"path", path, "group", group, "services", services)
	republish(req.Runtime)
}

// listeningPorts is the set of ports the snapshot this call read saw listening.
func listeningPorts(snap state.Snapshot) map[int]bool {
	out := make(map[int]bool, len(snap.Ports))
	for _, port := range snap.Ports {
		out[port.Port] = true
	}
	return out
}

// initRoot validates the caller's root and resolves it the way `oberth init`
// resolves its own working directory: the git root above it when there is one,
// so the file lands next to `.git` and matches the group the resolver already
// publishes.
func initRoot(raw string) (string, error) {
	abs, err := absoluteRoot(raw)
	if err != nil {
		return "", err
	}
	if err := existingDirectory(abs, raw); err != nil {
		return "", err
	}
	// The git root above the named directory wins, so the file lands next to
	// `.git` and the group it creates is the one the resolver publishes for
	// everything inside that checkout.
	if root, _, ok := groups.Find(abs); ok {
		return root, nil
	}
	return abs, nil
}

// absoluteRoot is the caller's root as one cleaned absolute path. The empty
// spelling is refused rather than defaulted: the daemon's own working directory
// is not the caller's project.
func absoluteRoot(raw string) (string, error) {
	named := strings.TrimSpace(raw)
	if named == "" {
		return "", rpc.NewError(rpc.CodeInvalidParams, "root_dir is required",
			`the daemon has no working directory of yours; send {"root_dir": "/path/to/repo"}`)
	}
	if !filepath.IsAbs(named) {
		return "", rpc.NewError(rpc.CodeInvalidParams, "root_dir must be an absolute path: "+named,
			"resolve it against the caller's working directory first")
	}
	return groups.Canonical(named), nil
}

// existingDirectory refuses a root that is not there, or that is a file. The
// raw spelling is what the caller sees in the refusal, because that is the
// string they sent.
func existingDirectory(abs, raw string) error {
	info, err := os.Stat(abs)
	if err != nil {
		return rpc.NewError(rpc.CodeNotFound, "no such directory: "+strings.TrimSpace(raw),
			"root_dir is the project directory the "+groups.ConfigName+" goes in")
	}
	if !info.IsDir() {
		return rpc.NewError(rpc.CodeInvalidParams, strings.TrimSpace(raw)+" is not a directory",
			"root_dir is the project directory, not the "+groups.ConfigName+" itself")
	}
	return nil
}

// initSnapshot is the state the proposal is built from: the same cache
// `groups.list` reads, refreshed when nobody is keeping it warm.
//
// A warm cache is only trusted while a subscriber is reading it — that is the
// one reader whose deltas would otherwise disagree with the file this call is
// about to write.
func initSnapshot(rt *Runtime) (state.Snapshot, error) {
	if warm := rt.Scanner.Cached(); warm.Seq > 0 && rt.Subscribers() > 0 {
		return warm, nil
	}
	snap, err := rt.Scanner.Snapshot(scanner.Include{})
	if err != nil {
		return state.Snapshot{}, scanFailure(err)
	}
	return snap, nil
}

// serviceNames is the affected list: this method mutates a file, so what it
// reports is service names, exactly as `groups.config.set` does.
func serviceNames(cfg *groups.Config) []string {
	names := make([]string, 0, len(cfg.Services))
	for _, service := range cfg.Services {
		names = append(names, service.Name)
	}
	return names
}

// proposalGroup renders the config this call would write as the group row it
// produces, with each service that is listening right now marked running.
//
// A declared-but-down service is the normal case for a curated or merged file,
// which is why the row carries the distinction at all; for a plain proposal
// every service came from a listening port, so nothing there changes.
func proposalGroup(cfg *groups.Config, listening map[int]bool) state.Group {
	g := fileGroup(cfg)
	up := markListening(g.Services, listening, &g.Members)
	g.Status = groupStatus(up, len(g.Services))
	return g
}

// fileGroup is a manifest as the group row it stands for, before anyone asks
// which of its services are up: the name, where the file is, and the services
// exactly as the manifest lists them.
func fileGroup(cfg *groups.Config) state.Group {
	dir, path := cfg.Dir, cfg.Path
	return state.Group{
		Name:       cfg.Name,
		Source:     state.SourceFile,
		RootDir:    &dir,
		ConfigPath: &path,
		Services:   groups.ServiceRows(cfg),
		Members:    []int{},
	}
}

// markListening marks the services whose port is listening as running, adds
// those ports to members, and returns how many services are up.
func markListening(services []state.Service, listening map[int]bool, members *[]int) int {
	up := 0
	for i := range services {
		if services[i].Port == nil || !listening[*services[i].Port] {
			continue
		}
		port := *services[i].Port
		services[i].Running, services[i].PortActual = true, &port
		*members = append(*members, port)
		up++
	}
	sort.Ints(*members)
	return up
}

// groupStatus is the group's own status word for a given service count.
func groupStatus(up, total int) string {
	switch {
	case up == 0:
		return "stopped"
	case up == total:
		return "running"
	default:
		return "partial"
	}
}
