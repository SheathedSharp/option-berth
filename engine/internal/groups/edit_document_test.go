package groups

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEditRejectsTrailingDocumentsWithoutDataLoss(t *testing.T) {
	for _, tail := range []string{"---\nname: second\n", "---\n", "---\nservices: [\n"} {
		path := filepath.Join(t.TempDir(), ConfigName)
		original := "name: demo\nservices:\n  - name: api\n    cmd: echo api\n" + tail
		if err := os.WriteFile(path, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := EditServices(path, ConfigEdit{Add: []ServiceAdd{{Name: "worker", Cmd: "echo worker"}}}); err == nil {
			t.Error("edited only the first YAML document and discarded the rest")
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != original {
			t.Fatal("failed edit changed original file", err)
		}
	}
}
func TestEditSingleDocumentCanStillRepairInvalidName(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigName)
	if err := os.WriteFile(path, []byte("# keep\nname: two words\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := SetConfigName(path, "fixed"); err != nil || cfg.Name != "fixed" {
		t.Fatalf("validating old semantic state blocked repair: %v", err)
	}
}
