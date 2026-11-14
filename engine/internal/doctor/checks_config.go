package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"gopkg.in/yaml.v3"
)

// BrokenSuffix is how `oberth doctor --fix` names the file it moves aside:
// config.yaml.broken-20260906T121314Z. Nothing is ever deleted.
const BrokenSuffix = ".broken-"

// checkConfigParses reads the user's own config.yaml. A syntax error here is
// dangerous precisely because it is quiet: config.Load drops the file and
// carries on with defaults. The row therefore fails loudly and points at the
// exact bracket.
func checkConfigParses(_ context.Context, env *Env) rpc.DoctorCheck {
	path := env.ConfigPath
	if path == "" {
		return skip("no config path resolved", "")
	}
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return ok("no config file, defaults apply",
			path+" does not exist, which is a valid state")
	case err != nil:
		return fail("cannot read the config file", err.Error(), "")
	}
	if row, refused := parseConfig(path, data); refused {
		return row
	}
	return ok("config.yaml parses", path)
}

// parseConfig separates the two YAML problems a config file can have: text
// that is not YAML at all (repairable by moving the file aside), and YAML whose
// shapes do not fit config.Config (a person has to correct it).
func parseConfig(path string, data []byte) (rpc.DoctorCheck, bool) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		source := string(data)
		pos := parsePosition(source, err)
		where := path
		if pos.Line > 0 {
			where = fmt.Sprintf("%s:%d:%d", path, pos.Line, pos.Col)
		}
		detail := fmt.Sprintf("%s: %s", where, yamlMessage(err))
		if marked := caret(source, pos); marked != "" {
			detail += "\n" + marked
		}
		fix := fmt.Sprintf("fix %s, or run `oberth doctor --fix` to move it to %s%s<timestamp> and write a fresh template",
			path, filepath.Base(path), BrokenSuffix)
		return failFixable("config.yaml does not parse", detail, fix), true
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return warn("config.yaml parses but some settings have the wrong type",
			fmt.Sprintf("%s: %v", path, err), "oberth config edit"), true
	}
	return rpc.DoctorCheck{}, false
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
			fmt.Sprintf("chmod u+w %s (the runtime service writes its log, lock and database here)", dir))
	}
	probe.Close()
	os.Remove(probe.Name())
	return ok("config directory is writable", dir)
}

// checkProjectConfig looks for this project's manifest, first in the directory
// the caller named and then at every ancestor up to the git root — the same
// search every other command performs.
func checkProjectConfig(_ context.Context, env *Env) rpc.DoctorCheck {
	if env.Project == "" {
		return skip("no project directory", "")
	}
	dirs := projectConfigDirs(env.Project)
	if files := nearestManifest(dirs); len(files) > 0 {
		return projectConfigResult(files[0], files[1:])
	}
	return missingProjectConfig(env.Project, dirs)
}

// nearestManifest returns the files of the first directory that holds a
// manifest, or nil when none does.
func nearestManifest(dirs []string) []string {
	for _, dir := range dirs {
		if files := groups.FilesIn(dir); len(files) > 0 {
			return files
		}
	}
	return nil
}

// missingProjectConfig reports the paths that were looked at, not just the
// absence, so a person can tell a misspelling from a wrong directory.
func missingProjectConfig(project string, dirs []string) rpc.DoctorCheck {
	names := groups.ConfigNames()
	looked := make([]string, 0, len(dirs)*len(names))
	for _, dir := range dirs {
		for _, name := range names {
			looked = append(looked, filepath.Join(dir, name))
		}
	}
	detail := "looked at " + strings.Join(looked, ", ")
	if gitRootOf(project) == "" {
		detail += fmt.Sprintf("\n%s is not inside a git repository, so there is no repository root to look at either", project)
	}
	return warn("no "+groups.ConfigName+" for this project", detail,
		"oberth init writes one from what is listening right now")
}

// projectConfigResult reports the manifest option-berth reads for a project.
// shadowed holds the other spellings sitting next to it, which option-berth
// ignores; a file under the old dotfile name still works, and the row says how
// to rename it.
func projectConfigResult(path string, shadowed []string) rpc.DoctorCheck {
	cfg, warnings, err := groups.LoadWithWarnings(path)
	if err != nil {
		return fail(filepath.Base(path)+" does not load", err.Error(), "edit "+path)
	}
	if len(warnings) > 0 {
		return warn("manifest contains ignored fields", joinWarnings(warnings), "edit "+path)
	}
	summary := fmt.Sprintf("group %q, %d %s", cfg.Name, len(cfg.Services),
		plural(len(cfg.Services), "service"))
	base := filepath.Base(path)

	if len(shadowed) > 0 {
		ignored := baseNames(shadowed)
		return warn(fmt.Sprintf("%s, but %s is ignored next to %s", summary, strings.Join(ignored, " and "), base),
			fmt.Sprintf("option-berth reads %s and ignores %s", path, strings.Join(shadowed, ", ")),
			"move anything you still need into "+base+", then delete "+strings.Join(ignored, " and "))
	}
	if groups.IsLegacyName(base) {
		return warnFixable(fmt.Sprintf("%s, under the old name %s", summary, base),
			path+" still works; new projects get "+groups.ConfigName,
			"oberth doctor --fix --only project_config")
	}
	return ok(summary, path)
}

func joinWarnings(warnings []groups.FieldWarning) string {
	lines := make([]string, len(warnings))
	for i, warning := range warnings {
		lines[i] = warning.String()
	}
	return strings.Join(lines, "\n")
}

func baseNames(paths []string) []string {
	out := make([]string, len(paths))
	for i, path := range paths {
		out[i] = filepath.Base(path)
	}
	return out
}

func plural(n int, singular string) string {
	switch n {
	case 1:
		return singular
	default:
		return singular + "s"
	}
}

// ProjectConfigFiles selects the nearest manifest within this checkout. Repair
// and diagnosis share the same search, including nested project directories.
func ProjectConfigFiles(dir string) []string {
	return nearestManifest(projectConfigDirs(dir))
}

// projectConfigDirs lists the directories the manifest search visits, from the
// project directory up to the git root. Outside a repository it visits only
// the project directory, because there is no root that bounds the walk.
func projectConfigDirs(dir string) []string {
	current := groups.Canonical(dir)
	if current == "" {
		return nil
	}
	root := gitRootOf(current)
	var dirs []string
	for {
		dirs = append(dirs, current)
		if root == "" || current == root {
			return dirs
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dirs
		}
		current = parent
	}
}
