package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"

	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// resolve installs the real world behind every seam the caller left empty.
//
// One rule per field, and the rule says only how to resolve that field. A
// caller that wants the standard run passes nothing; a test that wants a
// fixture passes exactly the field it cares about and leaves the rest to the
// same defaults every other command uses.
func (e *Env) resolve() {
	e.resolveProcess()
	e.resolvePaths()
	e.resolveSeams()
}

// resolveProcess fills the facts about the process running the checks.
func (e *Env) resolveProcess() {
	if e.Mode == "" {
		e.Mode = ModeCLI
	}
	if e.GOOS == "" {
		e.GOOS = runtime.GOOS
	}
	if !e.uidSet && e.UID == 0 {
		e.UID = os.Getuid()
	}
	if e.Home == "" {
		if home, err := os.UserHomeDir(); err == nil {
			e.Home = home
		}
	}
	if e.Project == "" {
		if wd, err := os.Getwd(); err == nil {
			e.Project = wd
		}
	}
}

// resolvePaths fills the files and the address the checks inspect.
func (e *Env) resolvePaths() {
	resolvePath(&e.ConfigPath, config.Path)
	resolvePath(&e.ConfigDir, daemon.ConfigDir)
	resolvePath(&e.SocketPath, daemon.SocketPath)
	resolvePath(&e.DBPath, store.Path)
	resolvePath(&e.SettingsPath, paths.AttnConfigPath)
}

// resolveSeams fills the functions a check calls to see something the doctor
// cannot see for itself. A seam nobody wired reports errNoProbe, so a check
// that needs it skips instead of reading an empty result as a fact.
func (e *Env) resolveSeams() {
	if e.Executable == nil {
		e.Executable = os.Executable
	}
	if e.LookPath == nil {
		e.LookPath = exec.LookPath
	}
	if e.Daemon == nil {
		e.Daemon = func(context.Context) DaemonInfo { return DaemonInfo{Err: errNoProbe} }
	}
	if e.Docker == nil {
		e.Docker = func(context.Context) (string, error) { return "", errNoProbe }
	}
	if e.ProbeObservation == nil {
		e.ProbeObservation = ports.ProbeListenerObservation
	}
}

// resolvePath installs a resolved default only where the caller left the field
// blank.
func resolvePath(field *string, fallback func() string) {
	if *field != "" {
		return
	}
	*field = fallback()
}

// errNoProbe is what an unwired seam reports: the caller gave the doctor no way
// to look, so the check that needs it skips.
var errNoProbe = errors.New("not available in this process")

// gitRootOf is the repository root above dir, or "" when dir is not in one.
func gitRootOf(dir string) string {
	if root, _, found := groups.Find(dir); found {
		return root
	}
	return ""
}
