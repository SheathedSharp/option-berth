package groups

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// AddressBook is the read-only address book one manifest can hand a command:
// its own services, its `machine:` references, and its `workspace:`
// references, keyed by the name a `${name.port}` or `${name.url}` reference
// uses.
//
// Unresolved holds a name that is declared but has no address right now — a
// `port: auto` service that is not running, or a workspace reference whose
// checkout cannot be read. Callers that actually use the name must refuse to
// run rather than substitute a literal `${...}`.
type AddressBook struct {
	// Worktree is the stable ${worktree} token for this manifest's checkout.
	Worktree string
	Ports    map[string]int
	// Unresolved maps a declared name to why it has no address.
	Unresolved map[string]string
}

// AddressBook resolves every address this manifest names against runtime
// groups. It only reads declarations and published runtime facts: it never
// allocates a port and never starts or stops anything.
func (c *Config) AddressBook(groups []state.Group) AddressBook {
	book := AddressBook{
		Worktree:   WorktreeTokenFor(c.Dir),
		Ports:      map[string]int{},
		Unresolved: map[string]string{},
	}
	for _, svc := range c.Services {
		if !svc.HasPort() {
			book.Unresolved[svc.Name] = fmt.Sprintf("service %s declares no port", svc.Name)
			continue
		}
		port, err := c.serviceAddress(svc.Name, groups)
		if err != nil {
			book.Unresolved[svc.Name] = err.Error()
			continue
		}
		book.Ports[svc.Name] = port
	}
	for _, m := range c.Machine {
		book.Ports[m.Name] = m.Port
	}
	for _, w := range c.Workspace {
		port, err := c.WorkspaceServiceAddress(w.Name, groups)
		if err != nil {
			book.Unresolved[w.Name] = err.Error()
			continue
		}
		book.Ports[w.Name] = port
	}
	return book
}

// MachineAddress is the fixed port a `machine:` reference is anchored to.
func (c *Config) MachineAddress(name string) (int, bool) {
	m, ok := c.MachineNamed(name)
	return m.Port, ok
}

// RuntimeAddress reports whether a name's address can only be known from
// runtime facts: a local `port: auto` service, or any workspace reference
// (whose service may itself use an assigned port). Callers use it to avoid a
// daemon read when every address in hand is already settled by the manifest.
func (c *Config) RuntimeAddress(name string) bool {
	if svc, ok := c.ServiceNamed(name); ok {
		return svc.PortAuto
	}
	_, ok := c.WorkspaceNamed(name)
	return ok
}

// WorkspaceServiceAddress resolves a `workspace:` reference to the address its
// service is declared on, or is actually listening on, in the other checkout.
// It is read-only: the referenced service is never started. A fixed port is
// returned even while that service is down, because it is the declared
// address; a `port: auto` service must be running, because only runtime facts
// know its address.
func (c *Config) WorkspaceServiceAddress(name string, groups []state.Group) (int, error) {
	ref, ok := c.WorkspaceNamed(name)
	if !ok {
		return 0, fmt.Errorf("no workspace reference named %q", name)
	}
	remote, err := c.workspaceConfig(ref)
	if err != nil {
		return 0, err
	}
	svc, ok := remote.ServiceNamed(ref.Service)
	if !ok {
		return 0, fmt.Errorf("%s declares no service %q", remote.Path, ref.Service)
	}
	if group := groupForConfig(groups, remote); group != nil {
		if port, running, found := serviceAddressInGroup(group, svc.Name); found && running && port > 0 {
			return port, nil
		}
	}
	switch {
	case svc.Port > 0:
		return svc.Port, nil
	case svc.PortAuto:
		return 0, fmt.Errorf("service %s in %s uses port: auto and is not running; start it in that worktree first", svc.Name, remote.Dir)
	default:
		return 0, fmt.Errorf("service %s in %s declares no port", svc.Name, remote.Dir)
	}
}

// serviceAddress is one of this manifest's own services. A running service
// answers with the port it actually holds; a fixed declaration answers while
// stopped; a stopped `port: auto` service has no address until it is started.
func (c *Config) serviceAddress(name string, groups []state.Group) (int, error) {
	svc, ok := c.ServiceNamed(name)
	if !ok {
		return 0, fmt.Errorf("no service named %q", name)
	}
	if group := groupForConfig(groups, c); group != nil {
		if port, running, found := serviceAddressInGroup(group, name); found && running && port > 0 {
			return port, nil
		}
	}
	switch {
	case svc.Port > 0:
		return svc.Port, nil
	case svc.PortAuto:
		return 0, fmt.Errorf("service %s uses port: auto and is not running; run `oberth up` first", name)
	default:
		return 0, fmt.Errorf("service %s declares no port", name)
	}
}

// workspaceConfig loads the manifest in the checkout a workspace reference
// names.
func (c *Config) workspaceConfig(ref WorkspaceRef) (*Config, error) {
	path := strings.TrimSpace(ref.Worktree)
	if path == "" {
		return nil, fmt.Errorf("workspace %s: worktree is empty", ref.Name)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.Dir, filepath.FromSlash(path))
	}
	path = Canonical(path)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("workspace %s: %s: %w", ref.Name, ref.Worktree, err)
	}
	if info.IsDir() {
		files := FilesIn(path)
		if len(files) == 0 {
			return nil, fmt.Errorf("workspace %s: no %s in %s", ref.Name, ConfigName, path)
		}
		path = files[0]
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workspace %s: %s is not a directory or manifest file", ref.Name, path)
	}
	remote, err := Load(path)
	if err != nil {
		return nil, fmt.Errorf("workspace %s: %w", ref.Name, err)
	}
	return remote, nil
}

// groupForConfig finds the published group for a manifest. ConfigPath is the
// exact key; RootDir is the fallback for a group whose config was published
// without a path (a scan that only saw the checkout).
func groupForConfig(groups []state.Group, cfg *Config) *state.Group {
	wantConfig := Canonical(cfg.Path)
	for i := range groups {
		if groups[i].ConfigPath != nil && Canonical(*groups[i].ConfigPath) == wantConfig {
			return &groups[i]
		}
	}
	wantDir := Canonical(cfg.Dir)
	for i := range groups {
		if groups[i].RootDir != nil && Canonical(*groups[i].RootDir) == wantDir {
			return &groups[i]
		}
	}
	return nil
}

// serviceAddressInGroup returns the port a published service row carries.
// running is true only for a service the group marks as live; a live service
// with no listener has port 0.
func serviceAddressInGroup(group *state.Group, name string) (port int, running, found bool) {
	if group == nil {
		return 0, false, false
	}
	for _, svc := range group.Services {
		if svc.Name != name {
			continue
		}
		if svc.PortActual != nil && *svc.PortActual > 0 {
			return *svc.PortActual, svc.Running, true
		}
		if svc.Port != nil {
			return *svc.Port, svc.Running, true
		}
		return 0, svc.Running, true
	}
	return 0, false, false
}
