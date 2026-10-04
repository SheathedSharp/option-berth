package groups

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRejectsDuplicateAutoPort(t *testing.T) {
	for _, ports := range []string{
		"port: auto\n    port: auto", "port: auto\n    port: 18080",
		"port: 18080\n    port: auto", "port: auto\n    port: null",
		"port: 18080\n    port: 19090",
	} {
		t.Run(strings.ReplaceAll(ports, "\n    ", "/"), func(t *testing.T) {
			data := "name: demo\nservices:\n  - name: api\n    cmd: echo api\n    " + ports + "\n"
			_, err := Parse(filepath.Join(t.TempDir(), ConfigName), []byte(data))
			var configErr *ConfigError
			if !errors.As(err, &configErr) || !strings.Contains(err.Error(), "port") || !strings.Contains(err.Error(), "line") {
				t.Fatalf("expected located duplicate-port ConfigError, got %v", err)
			}
		})
	}
}

func TestParseRejectsTrailingYAMLDocuments(t *testing.T) {
	for _, tail := range []string{"---\nname: second\n", "---\n", "---\nservices: [\n"} {
		_, err := Parse(filepath.Join(t.TempDir(), ConfigName), []byte("name: demo\n"+tail))
		var configErr *ConfigError
		if !errors.As(err, &configErr) {
			t.Fatalf("trailing document %q silently accepted: %v", tail, err)
		}
	}
}

func TestParseSingleDocumentStillAcceptsSupportedForms(t *testing.T) {
	for _, data := range []string{
		"", "# only a comment\n", "---\nname: demo\n...\n",
		"name: demo\nservices:\n  - name: api\n    cmd: echo api\n    port: auto\n",
		"name: demo\nservices:\n  - name: api\n    cmd: echo api\n    port: 18080\n",
	} {
		if _, err := Parse(filepath.Join(t.TempDir(), ConfigName), []byte(data)); err != nil {
			t.Fatalf("valid single document %q rejected: %v", data, err)
		}
	}
}
