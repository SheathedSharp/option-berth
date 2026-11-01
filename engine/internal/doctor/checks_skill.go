package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// skillInstall is one copy of the option-berth skill found under HOME:
// where it is, how it got there, and the version its SKILL.md declares.
type skillInstall struct {
	// Provider is the tool whose skill directory this is.
	Provider string
	// Path is the entry as installed: a symlink, a directory, or a plugin's
	// copy. It is reported verbatim, because which of those it is *is* the
	// finding.
	Path string
	// Link is true when the entry is a symlink into a checkout rather than a
	// copy. A link follows its checkout; a copy only moves when the plugin
	// market or the user moves it.
	Link bool
	// Version is what SKILL.md declares, or "" when it could not be read.
	Version string
	// Err says why Version is empty.
	Err string
}

// skillLocations are the per-provider user skill directories the skill's own
// README documents. They are read, never written: option-berth does not
// install, update or repair the skill.
var skillLocations = []struct {
	provider string
	parts    []string
}{
	{"claude", []string{".claude", "skills", "option-berth"}},
	{"codex", []string{".codex", "skills", "option-berth"}},
	{"opencode", []string{".config", "opencode", "skills", "option-berth"}},
}

// checkSkillVersion answers "is the skill installed here the same age as the
// copy somewhere else on this machine?". It is deliberately narrower than a
// release check: nothing here knows which version of the skill is current, so
// a single install is never called outdated. What it can prove offline is that
// two copies of one skill disagree, and that a link no longer resolves — both
// of which mean an agent is being taught a contract the engine does not have.
//
// No row is printed when nothing is installed: not having the skill is a
// choice, not a fault.
func checkSkillVersion(_ context.Context, env *Env) rpc.DoctorCheck {
	installs := findSkillInstalls(env.Home)
	if len(installs) == 0 {
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: "the agent skill is not installed here",
			Detail:  "install it from github.com/SheathedSharp/option-berth-skill if you want an agent taught this contract",
		}
	}

	var parts []string
	for _, in := range installs {
		parts = append(parts, describeSkillInstall(in))
	}
	detail := strings.Join(parts, "; ")

	if dangling := danglingSkillInstalls(env.Home); len(dangling) > 0 {
		return rpc.DoctorCheck{
			Status:  StatusWarn,
			Summary: "a skill link points at a checkout that is gone",
			Detail:  detail,
			Fix: fmt.Sprintf("repair the link, or reinstall the skill: %s",
				strings.Join(dangling, ", ")),
		}
	}
	if providers, versions := disagreeingSkillVersions(installs); len(providers) > 1 {
		return rpc.DoctorCheck{
			Status: StatusWarn,
			Summary: fmt.Sprintf("%d installed copies of the skill disagree on its version",
				len(providers)),
			Detail: detail,
			Fix: fmt.Sprintf("keep one version: %s declare %s; a link follows its checkout, a copy only "+
				"moves when the plugin is updated",
				strings.Join(providers, " and "), strings.Join(versions, " vs ")),
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusOK,
		Summary: "the installed skill copies agree",
		Detail:  detail,
	}
}

// findSkillInstalls reads every provider's skill entry under home. A provider
// with nothing at its documented path contributes no row.
func findSkillInstalls(home string) []skillInstall {
	if home == "" {
		return nil
	}
	var out []skillInstall
	for _, loc := range skillLocations {
		path := filepath.Join(append([]string{home}, loc.parts...)...)
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		in := skillInstall{Provider: loc.provider, Path: path, Link: info.Mode()&os.ModeSymlink != 0}
		version, err := skillVersion(filepath.Join(path, "SKILL.md"))
		if err != nil {
			in.Err = err.Error()
		} else {
			in.Version = version
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// danglingSkillInstalls names the links whose target no longer exists. os.Lstat
// above succeeds for a broken link, so this is the only way to tell a link that
// follows a checkout from one that follows a deleted one.
func danglingSkillInstalls(home string) []string {
	if home == "" {
		return nil
	}
	var out []string
	for _, loc := range skillLocations {
		path := filepath.Join(append([]string{home}, loc.parts...)...)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			target, _ := os.Readlink(path)
			out = append(out, fmt.Sprintf("%s -> %s", path, target))
		}
	}
	return out
}

// skillVersion reads metadata.version out of a SKILL.md frontmatter block.
//
// It parses the four lines it needs rather than pulling in a YAML dependency:
// the file is two fields of frontmatter and a body, and a check that reports
// on agent-facing text should not be the reason the engine grows a parser. A
// file whose frontmatter it cannot follow reports that, instead of guessing.
func skillVersion(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", fmt.Errorf("%s: no frontmatter block", filepath.Base(filepath.Dir(path)))
	}
	// The block has to close before a version inside it counts: a truncated
	// file is not a declaration, and the body below the frontmatter is prose
	// that may name versions for its own reasons.
	end := -1
	for i, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			end = i + 1
			break
		}
	}
	if end < 0 {
		return "", fmt.Errorf("%s: frontmatter block is not closed",
			filepath.Base(filepath.Dir(path)))
	}
	for _, line := range lines[1:end] {
		// Only the indented `version:` under `metadata:` counts; a top-level
		// `version:` would be a different field.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.TrimSpace(key) != "version" {
			continue
		}
		if v := strings.Trim(strings.TrimSpace(value), `"'`); v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: frontmatter declares no metadata.version",
		filepath.Base(filepath.Dir(path)))
}

// disagreeingSkillVersions returns the providers and versions when the
// installed copies do not all declare the same one. Copies whose version could
// not be read are left out: an unreadable file is a different finding, and
// calling it a disagreement would be a guess.
func disagreeingSkillVersions(installs []skillInstall) (providers, versions []string) {
	seen := map[string]bool{}
	for _, in := range installs {
		if in.Version == "" || seen[in.Version] {
			continue
		}
		seen[in.Version] = true
		providers = append(providers, in.Provider)
		versions = append(versions, in.Version)
	}
	if len(providers) < 2 {
		return nil, nil
	}
	return providers, versions
}

// describeSkillInstall is one install as the detail column shows it.
func describeSkillInstall(in skillInstall) string {
	kind := "copy"
	if in.Link {
		kind = "link"
	}
	version := in.Version
	if version == "" {
		version = "unreadable"
		if in.Err != "" {
			version = "unreadable: " + in.Err
		}
	}
	return fmt.Sprintf("%s: %s %s, version %s", in.Provider, kind, in.Path, version)
}
