package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func integrationProject(t *testing.T) *Env {
	t.Helper()
	env := fakeEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "oberth.yaml"), "name: demo\nservices: []\n")
	env.Project = dir
	return env
}

func TestAgentIntegrationWarnsWhenTheWorktreeNeverNamesOberth(t *testing.T) {
	env := integrationProject(t)
	write(t, filepath.Join(env.Project, "AGENTS.md"), "# Rules\n\nUse make test.\n")

	row := run(t, env, checkAgentIntegration)
	wantStatus(t, row, StatusWarn)
	if !strings.Contains(row.Summary, "agent instructions do not mention oberth") {
		t.Fatalf("summary = %q", row.Summary)
	}
	if !strings.Contains(row.Detail, "AGENTS.md") || !strings.Contains(row.Detail, "CLAUDE.md") {
		t.Fatalf("detail = %q, want both paths it looked at", row.Detail)
	}
	if !strings.Contains(row.Fix, "docs/integrations.md") {
		t.Fatalf("fix = %q, want the paste section", row.Fix)
	}
}

func TestAgentIntegrationAcceptsEitherInstructionFile(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		t.Run(name, func(t *testing.T) {
			env := integrationProject(t)
			write(t, filepath.Join(env.Project, name), "## Local services\n\nThis repo uses Oberth for its services.\n")
			row := run(t, env, checkAgentIntegration)
			wantStatus(t, row, StatusOK)
			if !strings.Contains(row.Detail, name) {
				t.Fatalf("detail = %q, want the file that mentioned it", row.Detail)
			}
		})
	}
}

func TestAgentIntegrationSkipsWhenNoManifestApplies(t *testing.T) {
	env := fakeEnv(t)
	env.Project = t.TempDir()
	row := run(t, env, checkAgentIntegration)
	wantStatus(t, row, StatusSkip)
	if !strings.Contains(row.Summary, "no oberth.yaml") {
		t.Fatalf("summary = %q", row.Summary)
	}
	if _, err := os.Stat(filepath.Join(env.Project, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("the check created an instruction file")
	}
}
