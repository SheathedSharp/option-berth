package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"gopkg.in/yaml.v3"
)

// BrokenSuffix is how `oberth doctor --fix` names the file it moves aside:
// config.yaml.broken-20260906T121314Z. Nothing is ever deleted.
const BrokenSuffix = ".broken-"

// checkConfigParses reads the user's own config.yaml. A file that does not
// parse is the one failure here that silently changes what every other command
// does — config.Load drops the whole file on a syntax error and carries on with
// defaults — so it fails loudly and points at the exact bracket.
func checkConfigParses(_ context.Context, env *Env) rpc.DoctorCheck {
	path := env.ConfigPath
	if path == "" {
		return skip("no config path resolved", "")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ok("no config file, defaults apply",
			path+" does not exist, which is a valid state")
	}
	if err != nil {
		return fail("cannot read the config file", err.Error(), "")
	}

	src := string(data)
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		pos := parsePosition(src, err)
		where := path
		if pos.Line > 0 {
			where = fmt.Sprintf("%s:%d:%d", path, pos.Line, pos.Col)
		}
		detail := fmt.Sprintf("%s: %s", where, yamlMessage(err))
		if marked := caret(src, pos); marked != "" {
			detail += "\n" + marked
		}
		return failFixable("config.yaml does not parse", detail,
			fmt.Sprintf("fix %s, or run `oberth doctor --fix` to move it to %s%s<timestamp> and write a fresh template",
				path, filepath.Base(path), BrokenSuffix))
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return warn("config.yaml parses but some settings have the wrong type",
			fmt.Sprintf("%s: %v", path, err), "oberth config edit")
	}
	return ok("config.yaml parses", path)
}

func checkConfigDirWritable(_ context.Context, env *Env) rpc.DoctorCheck {
	dir := env.ConfigDir
	if dir == "" {
		return skip("no config directory resolved", "")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail("cannot create the config directory", fmt.Sprintf("%s: %v", dir, err),
			"create it yourself, or point HOME somewhere writable")
	}
	probe, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return fail("config directory is not writable", fmt.Sprintf("%s: %v", dir, err),
			fmt.Sprintf("chmod u+w %s (the daemon writes its log, lock and database here)", dir))
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)
	return ok("config directory is writable", dir)
}

// checkProjectConfig looks for this project's `oberth.yaml`, in the directory
// the caller named and then at the git root above it — the two places every
// other command looks.
func checkProjectConfig(_ context.Context, env *Env) rpc.DoctorCheck {
	dir := env.Project
	if dir == "" {
		return skip("no project directory", "")
	}
	root := gitRootOf(dir)

	var looked []string
	for _, base := range projectConfigDirs(dir) {
		if base == "" {
			continue
		}
		if present := groups.FilesIn(base); len(present) > 0 {
			return projectConfigResult(present[0], present[1:])
		}
		for _, name := range groups.ConfigNames() {
			looked = append(looked, filepath.Join(base, name))
		}
		if root == dir {
			break
		}
	}

	detail := "looked at " + strings.Join(dedupe(looked), ", ")
	if root == "" {
		detail += fmt.Sprintf("\n%s is not inside a git repository, so there is no repository root to look at either", dir)
	}
	return warn("no "+groups.ConfigName+" for this project", detail,
		"oberth init writes one from what is listening right now")
}

// projectConfigResult reports the config option-berth reads for a project. shadowed
// are the other spellings sitting next to it, which option-berth ignores; a file under
// the old dotfile name still works, and the check says how to rename it.
func projectConfigResult(path string, shadowed []string) rpc.DoctorCheck {
	cfg, warnings, err := groups.LoadWithWarnings(path)
	if err != nil {
		return fail(filepath.Base(path)+" does not load", err.Error(), "edit "+path)
	}
	if len(warnings) > 0 {
		details := make([]string, 0, len(warnings))
		for _, warning := range warnings {
			details = append(details, warning.String())
		}
		return warn("manifest contains ignored fields", strings.Join(details, "\n"), "edit "+path)
	}
	summary := fmt.Sprintf("group %q, %d %s", cfg.Name, len(cfg.Services),
		plural(len(cfg.Services), "service"))
	base := filepath.Base(path)

	if len(shadowed) > 0 {
		names := make([]string, 0, len(shadowed))
		for _, s := range shadowed {
			names = append(names, filepath.Base(s))
		}
		return warn(fmt.Sprintf("%s, but %s is ignored next to %s", summary, strings.Join(names, " and "), base),
			fmt.Sprintf("option-berth reads %s and ignores %s", path, strings.Join(shadowed, ", ")),
			"move anything you still need into "+base+", then delete "+strings.Join(names, " and "))
	}
	if groups.IsLegacyName(base) {
		return warnFixable(fmt.Sprintf("%s, under the old name %s", summary, base),
			path+" still works; new projects get "+groups.ConfigName,
			"oberth doctor --fix --only project_config")
	}
	return ok(summary, path)
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// ProjectConfigFiles selects the nearest manifest within this checkout. Repair
// and diagnosis share the same selection, including nested project directories.
func ProjectConfigFiles(dir string) []string {
	for _, base := range projectConfigDirs(dir) {
		if files := groups.FilesIn(base); len(files) > 0 {
			return files
		}
	}
	return nil
}
func projectConfigDirs(dir string) []string {
	dir = groups.Canonical(dir)
	if dir == "" {
		return nil
	}
	root := gitRootOf(dir)
	var dirs []string
	for {
		dirs = append(dirs, dir)
		parent := filepath.Dir(dir)
		if root == "" || dir == root || parent == dir {
			break
		}
		dir = parent
	}
	return dirs
}
