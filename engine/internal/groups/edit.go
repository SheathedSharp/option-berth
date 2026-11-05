package groups

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"gopkg.in/yaml.v3"
)

// rootKeyOrder places a top-level key in a manifest. It mirrors keyOrder one
// level down: a `services:` list this package creates still lands where a
// hand-written file would put it.
var rootKeyOrder = []string{"name", "services", "ports", FieldWorktreePorts}

// IntChange is an edit to one optional integer key. The zero value leaves the
// key untouched; Set with a nil Value removes it; Set with a Value writes it.
// That is how the wire's absent / null / number reach the file.
type IntChange struct {
	Set bool

	Value *int
}

// SetInt writes n.
func SetInt(n int) IntChange { return IntChange{Set: true, Value: &n} }

// ClearInt removes the key.
func ClearInt() IntChange { return IntChange{Set: true} }

// ServiceAdd is a service to append to a `oberth.yaml`. It is the whole
// editable service, not a patch: an added service does not exist yet, so
// "absent" and "null" mean the same thing and the zero value of every field is
// simply left out of the file.
type ServiceAdd struct {
	Name          string   `json:"name"`
	Prepare       string   `json:"prepare,omitempty"`
	PrepareInputs []string `json:"prepare_inputs,omitempty"`
	Port          int      `json:"port,omitempty"`
	Cmd           string   `json:"cmd,omitempty"`
	Cwd           string   `json:"cwd,omitempty"`
	Sources       []string `json:"sources,omitempty"`
	Health        string   `json:"health,omitempty"`
	Description   string   `json:"description,omitempty"`
	Icon          string   `json:"icon,omitempty"`
	Color         string   `json:"color,omitempty"`
	DependsOn     []string `json:"depends_on,omitempty"`
}

// Service is the config entry the add describes.
func (a ServiceAdd) Service() Service {
	return Service{
		Name:          a.Name,
		Prepare:       a.Prepare,
		PrepareInputs: append([]string{}, a.PrepareInputs...),
		Cmd:           a.Cmd,
		Cwd:           a.Cwd,
		Sources:       cloneStrings(a.Sources),
		Port:          a.Port,
		Health:        a.Health,
		Description:   a.Description,
		Icon:          a.Icon,
		Color:         a.Color,
		DependsOn:     append([]string{}, a.DependsOn...),
	}
}

// AddFrom is the add entry that would recreate a service.
func AddFrom(s Service) ServiceAdd {
	return ServiceAdd{
		Name:          s.Name,
		Prepare:       s.Prepare,
		PrepareInputs: append([]string{}, s.PrepareInputs...),
		Port:          s.Port,
		Cmd:           s.Cmd,
		Cwd:           s.Cwd,
		Sources:       cloneStrings(s.Sources),
		Health:        s.Health,
		Description:   s.Description,
		Icon:          s.Icon,
		Color:         s.Color,
		DependsOn:     append([]string{}, s.DependsOn...),
	}
}

// AddsFrom turns a whole config into the add list a proposal merges into an
// existing file.
func AddsFrom(cfg *Config) []ServiceAdd {
	adds := make([]ServiceAdd, 0, len(cfg.Services))
	for _, service := range cfg.Services {
		adds = append(adds, AddFrom(service))
	}
	return adds
}

// ServiceRename renames a service and every depends_on entry naming it, so the
// file stays valid across the rename.
type ServiceRename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ConfigEdit is one atomic set of changes to a manifest. The lists are applied
// in declaration order — remove, rename, add, then metadata patches — and each
// step sees the file the previous step left. Nothing is written unless every
// step succeeds and the result still parses, so a failed edit leaves the file
// byte-identical.
//
// Removing first is what lets `{"remove": ["old"], "add": [{"name": "new",
// "port": 8000}]}` reuse a port: by the time the add is checked, the port has
// been released.
type ConfigEdit struct {
	// Structure.
	Remove []string        `json:"remove,omitempty"`
	Rename []ServiceRename `json:"rename,omitempty"`
	Add    []ServiceAdd    `json:"add,omitempty"`

	// Service metadata patches.
	Services []ServiceEdit `json:"services,omitempty"`

	// WorktreePorts sets or removes the top-level worktree_ports key. It runs
	// last and touches no service, so it never appears in Affected.
	WorktreePorts IntChange `json:"-"`
}

// Empty reports whether the edit asks for nothing.
func (e ConfigEdit) Empty() bool {
	if e.WorktreePorts.Set {
		return false
	}
	return len(e.Remove)+len(e.Rename)+len(e.Add)+len(e.Services) == 0
}

// Affected lists the service names the edit touched, in application order: a
// removed name, a rename's new name, an added name, a patched name. A mutating
// method reports it as `affected`.
func (e ConfigEdit) Affected() []string {
	names := make([]string, 0, len(e.Remove)+len(e.Rename)+len(e.Add)+len(e.Services))
	names = append(names, e.Remove...)
	for _, rename := range e.Rename {
		names = append(names, rename.To)
	}
	for _, add := range e.Add {
		names = append(names, add.Name)
	}
	for _, edit := range e.Services {
		names = append(names, edit.Name)
	}
	return names
}

// ServiceConflictError reports an add or rename that would give a file two
// services with one name, or two services claiming one port. It maps onto the
// contract's `conflict`.
type ServiceConflictError struct {
	Path string
	// Name is the service already holding the name or the port.
	Name string
	// Port is non-zero when the clash is a port rather than a name.
	Port int
}

func (e *ServiceConflictError) Error() string {
	if e.Port == 0 {
		return fmt.Sprintf("service %q already exists in %s", e.Name, e.Path)
	}
	return fmt.Sprintf("port %d is already service %q in %s", e.Port, e.Name, e.Path)
}

// RenderEdit applies an edit to the manifest at path and returns the bytes it
// would write, without writing them. It walks the YAML node tree rather than
// marshalling a struct, so comments, key order and the author's formatting
// survive.
//
// The returned Config is the file as it would then stand: it is parsed from the
// rendered bytes, so a later Load sees exactly what was validated here.
func RenderEdit(path string, edit ConfigEdit) ([]byte, *Config, error) {
	apply := func(abs string, root *yaml.Node) error {
		return applyEdit(abs, root, edit)
	}
	return render(path, apply)
}

// SetConfigName writes a new top-level `name:` into a `oberth.yaml` and returns
// the file as it then stands. It goes down the same node-level path as every
// other edit, so comments and formatting survive, and nothing is written
// unless the result validates. `groups.rename` renames a file project with it.
func SetConfigName(path, name string) (*Config, error) {
	rename := func(_ string, root *yaml.Node) error {
		setKeyIn(root, "name", strNode(name), rootKeyOrder)
		return nil
	}
	rendered, cfg, err := render(path, rename)
	if err != nil {
		return nil, err
	}
	if err := WriteConfigFile(cfg.Path, rendered); err != nil {
		return nil, err
	}
	return cfg, nil
}

// render reads path, lets apply change its node tree, and returns the bytes
// that would be written together with the config they parse to.
func render(path string, apply func(abs string, root *yaml.Node) error) ([]byte, *Config, error) {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		return nil, nil, err
	}
	doc, err := decodeSingleDocument(contents, resolved)
	if err != nil {
		return nil, nil, err
	}
	root := documentRoot(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, nil, &ConfigError{Path: resolved, Problems: []string{"the file is not a YAML mapping"}}
	}
	if err := apply(resolved, root); err != nil {
		return nil, nil, err
	}
	return renderAndParse(resolved, doc)
}

// decodeSingleDocument checks the document boundary before anything edits it.
// Checking only the rendered output would silently erase every document after
// the first.
func decodeSingleDocument(data []byte, abs string) (*yaml.Node, error) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&doc); err != nil {
		return nil, &ConfigError{Path: abs, Problems: []string{err.Error()}}
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		problem := "expected a single YAML document"
		if err != nil {
			problem += ": " + err.Error()
		}
		return nil, &ConfigError{Path: abs, Problems: []string{problem}}
	}
	return &doc, nil
}

// renderAndParse validates the bytes that would hit the disk, not the node
// tree, so a rename can still repair a name that only the final file shows.
func renderAndParse(abs string, doc *yaml.Node) ([]byte, *Config, error) {
	rendered, err := encode(doc)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := parse(abs, rendered)
	if err != nil {
		return nil, nil, err
	}
	return rendered, parsed, nil
}

// EditServices applies an edit and writes the file back. Nothing reaches the
// disk unless the whole edit succeeded and the result validates.
func EditServices(path string, edit ConfigEdit) (*Config, error) {
	rendered, cfg, err := RenderEdit(path, edit)
	if err != nil {
		return nil, err
	}
	if err := WriteConfigFile(cfg.Path, rendered); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEdit mutates the node tree in place and stops at the first problem.
// The caller writes only after nil returns, so a half-applied tree is thrown
// away instead of rendered.
func applyEdit(abs string, root *yaml.Node, edit ConfigEdit) error {
	if err := removeServices(abs, root, edit.Remove); err != nil {
		return err
	}
	if err := renameServices(abs, root, edit.Rename); err != nil {
		return err
	}
	if err := addServices(abs, root, edit.Add); err != nil {
		return err
	}
	if err := patchServices(root, abs, edit.Services); err != nil {
		return err
	}
	return applyWorktreePorts(root, edit.WorktreePorts)
}

func removeServices(abs string, root *yaml.Node, names []string) error {
	for _, name := range names {
		if err := removeService(abs, root, name); err != nil {
			return err
		}
	}
	return nil
}

func renameServices(abs string, root *yaml.Node, renames []ServiceRename) error {
	for _, rename := range renames {
		if err := renameService(abs, root, rename); err != nil {
			return err
		}
	}
	return nil
}

func addServices(abs string, root *yaml.Node, adds []ServiceAdd) error {
	for _, add := range adds {
		if err := addService(abs, root, add); err != nil {
			return err
		}
	}
	return nil
}

func patchServices(root *yaml.Node, abs string, edits []ServiceEdit) error {
	services := mappingValue(root, "services")
	for _, edit := range edits {
		// A named service is checked even for an empty patch: naming one that
		// is not there is a mistake, and an empty patch is no place to hide it.
		service := findService(services, edit.Name)
		if service == nil {
			return &ServiceNotFoundError{Path: abs, Name: edit.Name}
		}
		if edit.Patch.Empty() {
			continue
		}
		applyPatch(service, edit.Patch)
	}
	return nil
}

func applyWorktreePorts(root *yaml.Node, change IntChange) error {
	if !change.Set {
		return nil
	}
	if change.Value == nil {
		removeKey(root, FieldWorktreePorts)
		return nil
	}
	// setKeyIn edits an existing value in place, keeping any comment beside
	// `worktree_ports:` on that line.
	setKeyIn(root, FieldWorktreePorts, intNode(*change.Value), rootKeyOrder)
	return nil
}

// removeService drops a service from the list and from every other service's
// depends_on, so the file that is left still validates.
func removeService(abs string, root *yaml.Node, name string) error {
	list := mappingValue(root, "services")
	position := serviceIndex(list, name)
	if position < 0 {
		return &ServiceNotFoundError{Path: abs, Name: name}
	}
	list.Content = slices.Delete(list.Content, position, position+1)
	for _, item := range list.Content {
		if item.Kind == yaml.MappingNode {
			dropDependency(item, name)
		}
	}
	return nil
}

// renameService rewrites a service's name key and every reference to it.
func renameService(abs string, root *yaml.Node, rename ServiceRename) error {
	list := mappingValue(root, "services")
	service := findService(list, rename.From)
	if service == nil {
		return &ServiceNotFoundError{Path: abs, Name: rename.From}
	}
	if rename.From != rename.To {
		if findService(list, rename.To) != nil {
			return &ServiceConflictError{Path: abs, Name: rename.To}
		}
	}
	// The name scalar is edited in place, so a comment on that line stays on
	// it and the key keeps its position in the mapping.
	if node := mappingValue(service, "name"); node != nil {
		node.Value = rename.To
	}
	for _, item := range list.Content {
		if item.Kind == yaml.MappingNode {
			renameDependency(item, rename.From, rename.To)
		}
	}
	return nil
}

// addService appends a service, refusing a name or a port the file already
// uses. A file with no `services:` key at all grows one.
func addService(abs string, root *yaml.Node, add ServiceAdd) error {
	return appendServiceNode(abs, root, add.Service(), serviceNode(add))
}

func appendServiceNode(abs string, root *yaml.Node, add Service, node *yaml.Node) error {
	services, err := serviceSequence(abs, root)
	if err != nil {
		return err
	}
	if findService(services, add.Name) != nil {
		return &ServiceConflictError{Path: abs, Name: add.Name}
	}
	if add.Port != 0 {
		if owner := servicePortHolder(services, add.Port); owner != "" {
			return &ServiceConflictError{Path: abs, Name: owner, Port: add.Port}
		}
	}
	services.Content = append(services.Content, node)
	return nil
}

// serviceSequence returns the `services:` list, creating it when the file has
// none. A `services:` that is not a list is refused rather than guessed at.
func serviceSequence(abs string, root *yaml.Node) (*yaml.Node, error) {
	services := mappingValue(root, "services")
	if services == nil {
		created := &yaml.Node{Kind: yaml.SequenceNode}
		setKeyIn(root, "services", created, rootKeyOrder)
		return created, nil
	}
	if services.Kind != yaml.SequenceNode {
		return nil, &ConfigError{Path: abs, Problems: []string{"services is not a list"}}
	}
	return services, nil
}

// serviceNode renders one added service as the mapping the encoder writes.
// Keys go in through setKey, so the order matches what `oberth init` produces
// and what a hand-written file looks like.
func serviceNode(add ServiceAdd) *yaml.Node {
	mapping := &yaml.Node{Kind: yaml.MappingNode}
	setText(mapping, "name", add.Name)
	setText(mapping, "prepare", add.Prepare)
	setFlowList(mapping, "prepare_inputs", add.PrepareInputs)
	setText(mapping, "cmd", add.Cmd)
	setText(mapping, "cwd", add.Cwd)
	// A non-nil empty `sources:` is meaningful: it disables the code check.
	if add.Sources != nil {
		setKey(mapping, "sources", flowList(add.Sources))
	}
	if add.Port != 0 {
		setKey(mapping, FieldPort, intNode(add.Port))
	}
	if add.Health != "" {
		setKey(mapping, FieldHealth, healthNode(ParseHealth(add.Health)))
	}
	setText(mapping, FieldDescription, add.Description)
	setText(mapping, FieldIcon, add.Icon)
	setText(mapping, FieldColor, add.Color)
	setFlowList(mapping, "depends_on", add.DependsOn)
	return mapping
}

func setText(mapping *yaml.Node, key, value string) {
	if value == "" {
		return
	}
	setKey(mapping, key, strNode(value))
}

func setFlowList(mapping *yaml.Node, key string, values []string) {
	if len(values) == 0 {
		return
	}
	setKey(mapping, key, flowList(values))
}

// flowList is the `[a, b]` spelling the manifest uses for short lists.
func flowList(values []string) *yaml.Node {
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, value := range values {
		sequence.Content = append(sequence.Content, strNode(value))
	}
	return sequence
}

func strNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// serviceIndex is the position of a named service in the services sequence.
func serviceIndex(services *yaml.Node, name string) int {
	if services == nil || services.Kind != yaml.SequenceNode {
		return -1
	}
	for index, item := range services.Content {
		if item.Kind == yaml.MappingNode && mappingName(item) == name {
			return index
		}
	}
	return -1
}

// servicePortHolder names the service already declaring this port.
func servicePortHolder(services *yaml.Node, port int) string {
	if services == nil || services.Kind != yaml.SequenceNode {
		return ""
	}
	declared := strconv.Itoa(port)
	for _, item := range services.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		if node := mappingValue(item, FieldPort); node != nil && node.Value == declared {
			return mappingName(item)
		}
	}
	return ""
}

// dropDependency removes one name from a service's depends_on, and the key
// itself once nothing is left in it.
func dropDependency(service *yaml.Node, name string) {
	dependencies := mappingValue(service, "depends_on")
	if dependencies == nil || dependencies.Kind != yaml.SequenceNode {
		return
	}
	before := len(dependencies.Content)
	dependencies.Content = slices.DeleteFunc(dependencies.Content, func(dep *yaml.Node) bool {
		return dep.Value == name
	})
	if len(dependencies.Content) == before {
		return
	}
	if len(dependencies.Content) == 0 {
		removeKey(service, "depends_on")
	}
}

// renameDependency rewrites the depends_on entries naming from.
func renameDependency(service *yaml.Node, from, to string) {
	dependencies := mappingValue(service, "depends_on")
	if dependencies == nil || dependencies.Kind != yaml.SequenceNode {
		return
	}
	for _, entry := range dependencies.Content {
		if entry.Value == from {
			entry.Value = to
		}
	}
}

// Curate replaces a proposal's services with the caller's list, which is how
// `groups.init` writes a curated file in one call. An entry naming a port the
// proposal also found keeps that proposal's cmd and cwd, so a client editing
// only names, ports and metadata does not throw away the command `oberth up`
// needs to start the service again.
func Curate(cfg *Config, services []ServiceAdd) *Config {
	curated := *cfg
	curated.Services = make([]Service, 0, len(services))
	for _, add := range services {
		service := add.Service()
		inheritExecution(&service, cfg, add.Port)
		curated.Services = append(curated.Services, service)
	}
	return &curated
}

// inheritExecution keeps the proposed command for a service the caller
// curated by port but did not describe. It fills only fields the caller left
// empty.
func inheritExecution(service *Service, cfg *Config, port int) {
	if service.Cmd != "" && service.Cwd != "" {
		return
	}
	if port == 0 {
		return
	}
	for _, proposed := range cfg.Services {
		if proposed.Port != port {
			continue
		}
		if service.Cmd == "" {
			service.Cmd = proposed.Cmd
		}
		if service.Cwd == "" {
			service.Cwd = proposed.Cwd
		}
		return
	}
}

// RenderServiceMerge uses fully parsed services for local draft adoption. The
// public ServiceAdd RPC shape stays narrower on purpose; draft-only execution
// fields must not travel through it.
func RenderServiceMerge(path string, services []Service) ([]byte, *Config, error) {
	merge := func(abs string, root *yaml.Node) error {
		for _, service := range services {
			var node yaml.Node
			if err := node.Encode(service); err != nil {
				return err
			}
			if err := appendServiceNode(abs, root, service, &node); err != nil {
				return err
			}
		}
		return nil
	}
	return render(path, merge)
}
