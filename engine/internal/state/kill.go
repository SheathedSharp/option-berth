package state

// KillMethod is what was done — or, for a dry run, what would be done — to one
// process or container. The vocabulary comes from the kill contract: sigterm
// and sigkill are distinct so a caller can see whether escalation happened.
type KillMethod string

const (
	// MethodSIGTERM is the polite stop: SIGTERM on unix, `taskkill` without
	// /F on Windows.
	MethodSIGTERM KillMethod = "sigterm"

	// MethodSIGKILL is the unconditional stop: SIGKILL on unix, `taskkill /F`
	// on Windows. It is either requested with --force or reached by
	// escalation.
	MethodSIGKILL KillMethod = "sigkill"

	// MethodDockerStop is `docker stop <container>`.
	MethodDockerStop KillMethod = "docker_stop"

	// MethodNone means nothing happened: the target could not be resolved,
	// or it was already gone.
	MethodNone KillMethod = "none"
)

// AllKillMethods is the enum used by schema generation.
var AllKillMethods = []KillMethod{
	MethodSIGTERM, MethodSIGKILL, MethodDockerStop, MethodNone,
}

// KillResult is one row of a kill-shaped result: what happened to a single
// process or container. A tree kill emits one row per signalled process,
// children before parents, each carrying the port of the listener whose tree
// it belongs to.
type KillResult struct {
	// Which listener this row belongs to.
	Host        string `json:"host"`
	Port        int    `json:"port"`
	BindAddress string `json:"bind_address"`

	// What was signalled and how.
	PID    int        `json:"pid"`
	Name   string     `json:"name"`
	Method KillMethod `json:"method" jsonschema:"enum=sigterm,enum=sigkill,enum=docker_stop,enum=none"`
	OK     bool       `json:"ok"`
	Error  string     `json:"error,omitempty"`
	// Code is the contract code behind Error (for example permission_denied
	// or not_found). A reader can branch on it without parsing prose, and
	// not_found says something ok:false cannot: the thing was already gone,
	// which is the end state the caller asked for.
	Code string `json:"code,omitempty"`
}

// Key is the port key this row affected. A target addressed by PID alone has
// no port, and therefore no key.
func (r KillResult) Key() string {
	switch {
	case r.Port == 0:
		return ""
	}
	return Port{Port: r.Port, BindAddress: r.BindAddress}.Key()
}
