package daemon

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// Read/write namespace for `oberth.yaml`, together with the reload that keeps
// the daemon's long-lived index in step with what is on disk. `groups.start`
// is deliberately elsewhere (internal/daemon/groupstart): starting a service
// needs the run registry, and this package must not import it.
func init() {
	registerConfigHandlers()
	registerGroupCapabilities()
}

func registerConfigHandlers() {
	RegisterHandler("groups.config.get", handleGroupsConfigGet)
	RegisterHandler("groups.config.set", handleGroupsConfigSet)
	RegisterHandler("groups.reload", handleGroupsReload)
}

func registerGroupCapabilities() {
	RegisterCapability("groups")
	RegisterCapability(rpc.CapabilityAutoPorts)
	RegisterCapability(rpc.CapabilityManifestExtras)
	RegisterCapability(rpc.CapabilityReadyTimeout)
}

func handleGroupsConfigGet(_ context.Context, req *Request) (any, error) {
	var p rpc.GroupsConfigGetParams
	err := req.Bind(&p)
	if err != nil {
		return nil, err
	}
	cfg, err := resolveConfig(req.Runtime, p.Name, p.Path)
	if err != nil {
		return nil, err
	}
	result := rpc.GroupsConfigGetResult{
		Path:   cfg.Path,
		Config: configRow(req.Runtime, cfg),
	}
	return result, nil
}

func handleGroupsConfigSet(_ context.Context, req *Request) (any, error) {
	var p rpc.GroupsConfigSetParams
	err := req.Bind(&p)
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(p.Path)
	if path == "" {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "path is required",
			`send {"path": "/repo/oberth.yaml", "services": [{"name": "api", "patch": {"icon": "server"}}]}`)
	}
	edit := configEditFrom(p)
	if empty := edit.Empty(); empty {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "services, add, rename, remove or worktree_ports is required",
			`send {"path": "…", "add": [{"name": "worker", "port": 9000}]}, or a rename, a remove, `+
				`{"services": [{"name": "api", "patch": {...}}]} or {"worktree_ports": 4}`)
	}
	if err = checkEditNames(edit); err != nil {
		return nil, err
	}
	err = checkConfigPath(path)
	if err != nil {
		return nil, err
	}
	// An edit is applied to the file's own node tree and re-rendered, which is
	// what keeps comments, key order and formatting alive. One exception:
	// yaml.v3 preserves the text of a trailing comment but not the padding in
	// front of it, so `cmd: x     # note` returns as `cmd: x # note`. A
	// LineComment node carries no column and the emitter always writes a single
	// space; keeping the original padding would mean rendering the file by
	// hand. The comment itself never disappears, which is the line drawn here.
	//
	// Nothing reaches the disk before the whole edit has been applied and
	// validated on that tree, so a rename that clashes half way through a
	// batch leaves the file untouched.
	cfg, err := groups.EditServices(path, edit)
	if err != nil {
		return nil, configError(path, err)
	}

	// The file just written is re-indexed and republished, so the edit shows up
	// as a groups delta for subscribers before the caller reads again.
	affected := edit.Affected()
	err = req.Runtime.Scanner.LoadConfig(cfg.Path)
	if err != nil {
		req.Runtime.Logger.Warn("reloading a config after writing it", "path", cfg.Path, "error", err)
	}
	req.Runtime.Logger.Info("config written", "path", cfg.Path, "services", len(affected))
	republish(req.Runtime)

	result := rpc.GroupsConfigSetResult{
		MutationResult: rpc.MutationResult{OK: true, Affected: affected},
		Path:           cfg.Path,
		Config:         configRow(req.Runtime, cfg),
	}
	return result, nil
}

// configEditFrom carries the request's edit fields into the groups package's
// edit value. `worktree_ports` is optional on the wire, so its change is
// computed rather than copied.
func configEditFrom(p rpc.GroupsConfigSetParams) groups.ConfigEdit {
	return groups.ConfigEdit{
		Add:      p.Add,
		Services: p.Services,
		Remove:   p.Remove,
		Rename:   p.Rename,

		WorktreePorts: p.WorktreePortsChange(),
	}
}

// checkEditNames refuses a malformed edit before the disk is touched at all.
// A nameless service is a caller mistake rather than a config problem, so it
// earns `invalid_params` and not the `invalid_config` that writing the name
// would produce.
func checkEditNames(edit groups.ConfigEdit) error {
	nameless := func(s string) bool { return strings.TrimSpace(s) == "" }
	for _, name := range edit.Remove {
		if nameless(name) {
			return rpc.NewError(rpc.CodeInvalidParams, "remove names an empty service",
				`each entry is the name of a service in the file: {"remove": ["worker"]}`)
		}
	}
	for _, r := range edit.Rename {
		if nameless(r.From) || nameless(r.To) {
			return rpc.NewError(rpc.CodeInvalidParams, "rename needs a from and a to",
				`each entry is {"from": "<current name>", "to": "<new name>"}`)
		}
	}
	for _, a := range edit.Add {
		if nameless(a.Name) {
			return rpc.NewError(rpc.CodeInvalidParams, "add names an empty service",
				`each entry is {"name": "worker", "port": 9000}`)
		}
	}
	for _, e := range edit.Services {
		if nameless(e.Name) {
			return rpc.NewError(rpc.CodeInvalidParams, "services names an empty service",
				`each entry is {"name": "<service>", "patch": {...}}; a null value clears a key`)
		}
	}
	return nil
}

func handleGroupsReload(_ context.Context, req *Request) (any, error) {
	loaded, rejected := req.Runtime.Scanner.ReloadConfigs()
	problems := make([]rpc.ConfigProblem, 0, len(rejected))
	for i := range rejected {
		problems = append(problems, rpc.ConfigProblem{
			Path:  rejected[i].Path,
			Error: rejected[i].Err.Error(),
		})
	}
	req.Runtime.Logger.Info("reloaded oberth.yaml files", "configs", loaded, "invalid", len(problems))
	republish(req.Runtime)
	return rpc.GroupsReloadResult{Loaded: loaded, Errors: problems}, nil
}

// resolveConfig turns "path or group name" into the config a request names.
// A path the index has never seen is loaded there and then, so the desktop can
// open a project the daemon has not scanned a process in.
func resolveConfig(rt *Runtime, name, path *string) (*groups.Config, error) {
	if path != nil {
		if p := strings.TrimSpace(*path); p != "" {
			return configAtPath(rt, p)
		}
	}
	if name != nil {
		if n := strings.TrimSpace(*name); n != "" {
			return configNamed(rt, n)
		}
	}
	return nil, rpc.NewError(rpc.CodeInvalidParams, "name or path is required",
		`send {"name": "my-app"} or {"path": "/repo/oberth.yaml"}`)
}

func configAtPath(rt *Runtime, p string) (*groups.Config, error) {
	if err := checkConfigPath(p); err != nil {
		return nil, err
	}
	cfg, indexed := rt.Scanner.ConfigAt(p)
	if indexed {
		return cfg, nil
	}
	if err := rt.Scanner.LoadConfig(p); err != nil {
		return nil, configError(p, err)
	}
	cfg, indexed = rt.Scanner.ConfigAt(p)
	if !indexed {
		return nil, rpc.NewError(rpc.CodeNotFound, "no usable "+groups.ConfigName+" at "+p,
			"run `oberth status` inside a worktree with a manifest")
	}
	return cfg, nil
}

func configNamed(rt *Runtime, n string) (*groups.Config, error) {
	if cfg, ok := rt.Scanner.ConfigNamed(n); ok {
		return cfg, nil
	}
	return nil, rpc.NewError(rpc.CodeNotFound, "no project named "+n+" has a "+groups.ConfigName,
		"run `oberth status` inside a worktree with a manifest; `groups.reload` re-reads it")
}

// checkConfigPath refuses to read or write anything that is not a `oberth.yaml`.
// The daemon writes files because a client asked it to, so the single filename
// it will touch is fixed here instead of being taken from the caller.
func checkConfigPath(path string) error {
	if groups.IsConfigName(filepath.Base(path)) {
		return nil
	}
	return rpc.NewError(rpc.CodeInvalidParams,
		path+" is not a "+groups.ConfigName,
		"option-berth only reads and writes "+groups.ConfigName)
}

// configError maps a config failure onto the contract's error registry: a
// missing file or service is `not_found`; a name or port that an added or
// renamed service would take twice is `1011 conflict`; a file that no longer
// validates is `1006 invalid_config`, with the problems in data.detail.
func configError(path string, err error) error {
	var missing *groups.ServiceNotFoundError
	var clash *groups.ServiceConflictError
	var bad *groups.ConfigError
	switch {
	case errors.As(err, &missing):
		return rpc.NewError(rpc.CodeNotFound, missing.Error(),
			"`groups.config.get` lists the service names this file declares")
	case errors.As(err, &clash):
		hint := "pick another name, or rename the service that has it"
		if clash.Port != 0 {
			hint = "pick another port, or remove " + clash.Name + " in the same call"
		}
		return rpc.NewError(rpc.CodeConflict, clash.Error(), hint)
	case errors.As(err, &bad):
		return rpc.NewError(rpc.CodeInvalidConfig, bad.Error(),
			"the file was left as it was; fix the values and try again")
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist):
		return rpc.NewError(rpc.CodeNotFound, "no such file: "+path,
			"`oberth init` writes a "+groups.ConfigName+" at the repository root")
	}
	return rpc.NewError(rpc.CodeInternal, err.Error(), "check `oberth daemon log`")
}

// configRow renders a config for the wire. Running and port_actual are joined
// from state the daemon already holds, so an editor reading a config back sees
// the same service rows `groups.list` publishes.
func configRow(rt *Runtime, cfg *groups.Config) rpc.GroupConfig {
	out := rpc.GroupConfig{
		Ports:    append([]int{}, cfg.Ports...),
		Name:     cfg.Name,
		Services: groups.ServiceRows(cfg),
	}
	if out.Ports == nil {
		out.Ports = []int{}
	}
	if wp := cfg.WorktreePorts; wp != nil {
		// A copy, so the row never aliases the index's config.
		ports := *wp
		out.WorktreePorts = &ports
	}
	live := liveServices(rt, cfg.Name)
	for i, svc := range out.Services {
		if seen, ok := live[svc.Name]; ok {
			out.Services[i].Running = seen.Running
			out.Services[i].PortActual = seen.PortActual
		}
	}
	return out
}

// liveServices is the group's services as the last snapshot saw them.
func liveServices(rt *Runtime, name string) map[string]state.Service {
	live := make(map[string]state.Service)
	for _, g := range rt.Scanner.Cached().Groups {
		if g.Name == name {
			for _, svc := range g.Services {
				live[svc.Name] = svc
			}
		}
	}
	return live
}
