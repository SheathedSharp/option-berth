package restartout

import (
	"encoding/json"
	"strings"
	"testing"
)

// The JSON document carries the warnings computed before the stop, so a caller
// reading only the result still knows the restart did not rebuild anything.
func TestDocumentCarriesWarnings(t *testing.T) {
	files := 2
	raw, err := json.Marshal(Document{
		Warnings: []Warning{{Service: "api", CodeChanged: true, CodeChangedFiles: &files}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"warnings":[{"service":"api","code_changed":true,"code_changed_files":2}]`) {
		t.Fatalf("document = %s, want the warning facts", raw)
	}
}
