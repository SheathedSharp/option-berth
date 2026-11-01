package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill puts a SKILL.md with the given metadata.version under home at the
// provider's documented skill path and returns that path.
func writeSkill(t *testing.T, home, provider, version string) string {
	t.Helper()
	parts := map[string]string{
		"claude":   ".claude/skills/option-berth",
		"codex":    ".codex/skills/option-berth",
		"opencode": ".config/opencode/skills/option-berth",
	}[provider]
	if parts == "" {
		t.Fatalf("unknown provider %q", provider)
	}
	dir := filepath.Join(home, parts)
	write(t, filepath.Join(dir, "SKILL.md"), "---\nname: option-berth\nmetadata:\n  version: \""+version+"\"\n---\n\n# option-berth\n")
	return dir
}

func TestSkillVersionReadsOnlyTheNestedField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	// A top-level `version:` must not be mistaken for metadata.version.
	write(t, path, "---\nname: option-berth\nversion: \"9.9.9\"\nmetadata:\n  version: \"0.7.1\"\n---\n")
	got, err := skillVersion(path)
	if err != nil {
		t.Fatalf("skillVersion: %v", err)
	}
	if got != "0.7.1" {
		t.Errorf("version = %q, want the nested 0.7.1", got)
	}
}

func TestSkillVersionErrorsRatherThanGuessing(t *testing.T) {
	for name, content := range map[string]string{
		"no frontmatter":       "# option-berth\n",
		"unterminated":         "---\nname: option-berth\nmetadata:\n  version: \"0.7.1\"\n",
		"no metadata version":  "---\nname: option-berth\n---\n",
		"empty metadata value": "---\nmetadata:\n  version: \"\"\n---\n",
		"version is top-level": "---\nversion: \"0.7.1\"\n---\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "SKILL.md")
			write(t, path, content)
			if _, err := skillVersion(path); err == nil {
				t.Fatal("want an error rather than a version")
			}
		})
	}
}

func TestSkillVersionMissingFile(t *testing.T) {
	if _, err := skillVersion(filepath.Join(t.TempDir(), "SKILL.md")); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

func TestAgentSkillCheck(t *testing.T) {
	t.Run("nothing installed is a skip, not a warn", func(t *testing.T) {
		env := fakeEnv(t)
		got := run(t, env, checkSkillVersion)
		wantStatus(t, got, StatusSkip)
		if !strings.Contains(got.Summary, "not installed") {
			t.Errorf("summary = %q", got.Summary)
		}
	})

	t.Run("one install is never called outdated", func(t *testing.T) {
		env := fakeEnv(t)
		writeSkill(t, env.Home, "claude", "0.7.1")
		got := run(t, env, checkSkillVersion)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Detail, "version 0.7.1") {
			t.Errorf("detail = %q, want the version", got.Detail)
		}
		if got.Fix != "" {
			t.Errorf("fix = %q, want none: one copy has nothing to disagree with", got.Fix)
		}
	})

	t.Run("matching copies are ok", func(t *testing.T) {
		env := fakeEnv(t)
		writeSkill(t, env.Home, "claude", "0.7.1")
		writeSkill(t, env.Home, "codex", "0.7.1")
		got := run(t, env, checkSkillVersion)
		wantStatus(t, got, StatusOK)
	})

	t.Run("disagreeing copies warn with both versions", func(t *testing.T) {
		env := fakeEnv(t)
		writeSkill(t, env.Home, "claude", "0.7.1")
		writeSkill(t, env.Home, "codex", "0.6.0")
		got := run(t, env, checkSkillVersion)
		wantStatus(t, got, StatusWarn)
		if !strings.Contains(got.Summary, "disagree") {
			t.Errorf("summary = %q", got.Summary)
		}
		if !strings.Contains(got.Fix, "0.6.0") || !strings.Contains(got.Fix, "0.7.1") {
			t.Errorf("fix = %q, want both versions named", got.Fix)
		}
	})

	t.Run("an unreadable copy is not a disagreement", func(t *testing.T) {
		env := fakeEnv(t)
		writeSkill(t, env.Home, "claude", "0.7.1")
		write(t, filepath.Join(env.Home, ".codex/skills/option-berth/SKILL.md"), "# no frontmatter\n")
		got := run(t, env, checkSkillVersion)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Detail, "unreadable") {
			t.Errorf("detail = %q, want the unreadable copy called out", got.Detail)
		}
	})

	t.Run("a link into a live checkout is a link", func(t *testing.T) {
		env := fakeEnv(t)
		checkout := writeSkill(t, t.TempDir(), "claude", "0.7.1")
		link := filepath.Join(env.Home, ".claude/skills/option-berth")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(checkout, link); err != nil {
			t.Fatal(err)
		}
		got := run(t, env, checkSkillVersion)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Detail, "link") {
			t.Errorf("detail = %q, want the install called a link", got.Detail)
		}
	})

	t.Run("a dangling link warns", func(t *testing.T) {
		env := fakeEnv(t)
		link := filepath.Join(env.Home, ".claude/skills/option-berth")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "moved-away")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		got := run(t, env, checkSkillVersion)
		wantStatus(t, got, StatusWarn)
		if !strings.Contains(got.Fix, target) {
			t.Errorf("fix = %q, want the missing target named", got.Fix)
		}
	})
}

func TestAgentSkillIsSelectableOnItsOwn(t *testing.T) {
	env := *fakeEnv(t)
	got := Run(context.Background(), env, []string{"agent_skill"})
	if len(got.Checks) != 1 || got.Checks[0].ID != "agent_skill" {
		t.Fatalf("checks = %v, want only agent_skill", got.Checks)
	}
}

func TestAgentSkillCheckIsOffline(t *testing.T) {
	// The check must answer from HOME alone: no daemon, no network, no PATH.
	env := fakeEnv(t)
	env.Daemon = func(context.Context) DaemonInfo {
		t.Error("the skill check must not dial the daemon")
		return DaemonInfo{}
	}
	writeSkill(t, env.Home, "claude", "0.7.1")
	run(t, env, checkSkillVersion)
}
