package cmd

import (
	"github.com/sheathedsharp/option-berth/internal/groups"
	"os"
	"path/filepath"
	"testing"
)

func TestAdoptMergePreservesAutoPortAndEnvironment(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, groups.ConfigName)
	original := "# existing project comment\nname: demo\nservices:\n  - name: old\n    cmd: echo old\n"
	if err := os.WriteFile(target, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "draft.yaml")
	draft := "name: demo\nservices:\n  - name: api\n    cmd: python3 api.py\n    prepare: echo ready\n    port: auto\n    env:\n      MODE: fixture\n      EMPTY: \"\"\n"
	if err := os.WriteFile(source, []byte(draft), 0600); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if err := initAdoptDraft(root, source, false, true, true, false, true); err != nil {
			t.Fatal(err)
		}
	})
	if data, _ := os.ReadFile(target); string(data) != original {
		t.Fatal("dry run changed the target")
	}
	captureStdout(t, func() {
		if err := initAdoptDraft(root, source, false, true, false, false, true); err != nil {
			t.Fatal(err)
		}
	})
	cfg, err := groups.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Services) != 2 {
		t.Fatal(cfg.Services)
	}
	api := cfg.Services[1]
	if !api.PortAuto || api.Prepare != "echo ready" || api.Env["MODE"] != "fixture" {
		t.Fatalf("merge dropped execution fields: %+v", api)
	}
	if _, ok := api.Env["EMPTY"]; !ok {
		t.Fatal("merge dropped explicitly empty environment value")
	}
}
