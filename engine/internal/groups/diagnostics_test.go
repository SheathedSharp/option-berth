package groups

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestFieldWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		fields     []string
	}{
		{"known fields", "name: demo\nservices:\n  - name: api\n    cmd: echo api\n    port: auto\n    env: {CUSTOM: secret}\n", nil},
		{"machine typo", "name: demo\nmachine:\n  - name: db\n    port: 5432\n    prott: 5433\n", []string{"machine[0].prott"}},
		{"merged service anchor", "name: demo\nx-template: &defaults\n  cmd: echo api\n  heath: secret\nservices:\n  - <<: *defaults\n    name: api\n", []string{"services[0].heath"}},
		{"top-level typo", "name: demo\nservcies: []\n", []string{"servcies"}},
		{"comment only", "# intentionally empty\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ConfigName)
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			_, warnings, err := LoadWithWarnings(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != len(tc.fields) {
				t.Fatalf("warnings=%+v want=%v", warnings, tc.fields)
			}
			for i, w := range warnings {
				if w.Field != tc.fields[i] || w.Line < 1 || w.Column < 1 {
					t.Fatal(w)
				}
			}
		})
	}
}
func TestWarningsStillRejectInvalidDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigName)
	for _, data := range []string{"services: [", "name: demo\n---\nname: other\n", "name: demo\nservices: &cycle [*cycle]\n"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadWithWarnings(path); err == nil {
			t.Fatalf("accepted invalid document %q", data)
		}
	}
}
