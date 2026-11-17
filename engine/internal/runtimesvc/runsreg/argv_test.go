package runsreg

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runs"
)

// The exact vector is what makes a run restartable. It must survive the
// mirror and the recovery import; Cmd alone cannot be re-executed without
// guessing where the quotes were.
func TestArgvSurvivesTheMirrorAndRecovery(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	pid := os.Getpid()
	argv := []string{"npm", "run", "dev", "--", "--port", "3000"}
	first := testRegistry(pid)
	first.Mirror = true
	first.Register(Record{
		PID: pid, ID: "argv-run", Group: "demo", Name: "web",
		Argv: argv, Cmd: "npm run dev -- --port 3000",
		StartedAt: time.Now().UTC(),
	})
	entry, ok := runs.Load().LookupByPID(pid)
	if !ok || !reflect.DeepEqual(entry.Argv, argv) {
		t.Fatalf("mirror argv = %+v, want %+v", entry.Argv, argv)
	}

	second := testRegistry(pid)
	if moved, err := second.ImportLegacy(); err != nil || moved != 1 {
		t.Fatalf("ImportLegacy = %d/%v", moved, err)
	}
	rec, ok := second.Lookup(pid)
	if !ok || !reflect.DeepEqual(rec.Argv, argv) {
		t.Fatalf("imported argv = %+v, want %+v", rec.Argv, argv)
	}
}
