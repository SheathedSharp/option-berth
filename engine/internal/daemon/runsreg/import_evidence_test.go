package runsreg

import (
	"bytes"
	"os"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/runs"
)

// Uses the public call as a statement so the exact behavioral regression also
// builds against the historical count-only API. Return arity is not the proof.
func TestImportLegacyKeepsCorruptEvidence(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	original := []byte(`{"runs":`)
	if err := os.WriteFile(runs.Path(), original, 0o600); err != nil {
		t.Fatal(err)
	}
	r := New()
	r.ImportLegacy()
	after, err := os.ReadFile(runs.Path())
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("corrupt recovery evidence was discarded: %q, %v", after, err)
	}
	if _, found := r.Lookup(os.Getpid()); found {
		t.Fatal("failed recovery invented a run")
	}
}
