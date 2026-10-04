package groups

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceMergeKeepsReferencesAndRejectsConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigName)
	original := "# keep comment\nname: demo\nservices:\n  - name: existing\n    cmd: echo existing\n    port: 18080\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	additions := []Service{{Name: "api", Cmd: "python3 api.py", PortAuto: true, Env: map[string]string{"SELF": "${port}"}}, {Name: "worker", Cmd: "python3 worker.py", DependsOn: []string{"api"}, Env: map[string]string{"URL": "${api.url}"}}}
	out, cfg, err := RenderServiceMerge(path, additions)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "# keep comment") || !cfg.Services[1].PortAuto || cfg.Services[2].Env["URL"] != "${api.url}" {
		t.Fatalf("lossy merge: %s", out)
	}
	for _, bad := range []Service{{Name: "existing", Cmd: "echo duplicate"}, {Name: "other", Port: 18080, Cmd: "echo conflict"}} {
		if _, _, err := RenderServiceMerge(path, []Service{bad}); err == nil {
			t.Fatal("accepted conflicting addition")
		}
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatal("render changed the target")
	}
}
