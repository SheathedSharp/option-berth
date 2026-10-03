package ports

import (
	"encoding/json"
	"os"
	"testing"
)

// TestCaptureListeners is a manual harness for real-host verification, not an
// assertion: set BERTH_CAPTURE=<file> and it writes this machine's enriched
// listeners as JSON (port, pid, process, command, parent command, cwd, display
// name). Collection and identity changes are then diffed against the same
// machine before and after, which is what "真实主机采集必须验证" asks for.
// Without the variable it skips, so it never runs on its own.
func TestCaptureListeners(t *testing.T) {
	path := os.Getenv("BERTH_CAPTURE")
	if path == "" {
		t.Skip("set BERTH_CAPTURE=<file> to capture this machine's listeners")
	}
	pp, err := Scan()
	if err != nil {
		t.Fatal(err)
	}
	Enrich(pp)

	type row struct {
		Port    int    `json:"port"`
		PID     int    `json:"pid"`
		Process string `json:"process"`
		Command string `json:"command"`
		Parent  string `json:"parent_cmd"`
		Cwd     string `json:"cwd"`
		Display string `json:"display"`
	}
	out := make([]row, 0, len(pp))
	for _, p := range pp {
		out = append(out, row{p.Port, p.PID, p.Process, p.Command, p.ParentCmd, p.Cwd, p.DisplayName()})
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured %d listeners to %s", len(out), path)
}
