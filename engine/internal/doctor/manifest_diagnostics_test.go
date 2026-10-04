package doctor

import (
	"context"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsIgnoredManifestFieldsWithoutValues(t *testing.T) {
	env := fakeEnv(t)
	data := "name: demo\nservices:\n  - name: api\n    cmd: echo api\n    port: auto\n    heath: PRIVATE_VALUE_NOT_FOR_DIAGNOSTICS\n    depends_onn: []\n    env:\n      FREE_FORM_ENV: private-env-value\n"
	path := write(t, filepath.Join(env.Project, groups.ConfigName), data)
	got := run(t, env, checkProjectConfig)
	wantStatus(t, got, StatusWarn)
	for _, field := range []string{"services[0].heath", "services[0].depends_onn", "line 6"} {
		if !strings.Contains(got.Detail, field) {
			t.Fatalf("missing %s: %+v", field, got)
		}
	}
	for _, value := range []string{"PRIVATE_VALUE_NOT_FOR_DIAGNOSTICS", "private-env-value", "FREE_FORM_ENV"} {
		if strings.Contains(got.Detail, value) {
			t.Fatalf("diagnosis exposed value/env key: %s", value)
		}
	}
	if got.Fixable {
		t.Fatal("unknown fields must not be auto-repaired")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != data {
		t.Fatal("diagnosis changed manifest", err)
	}
}

func TestProjectOnlyDoctorDoesNotProbeDaemon(t *testing.T) {
	env := fakeEnv(t)
	write(t, filepath.Join(env.Project, groups.ConfigName), "name: demo\n")
	env.Daemon = func(context.Context) DaemonInfo {
		t.Error("project-only preflight contacted daemon")
		return DaemonInfo{}
	}
	result := Run(context.Background(), *env, []string{"project_config"})
	if len(result.Checks) != 1 {
		t.Fatal(result)
	}
	wantStatus(t, result.Checks[0], StatusOK)
}
