package groupstart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

func writePrepareFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareStampSkipsOnlyUnchangedInputs(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	dir := t.TempDir()
	writePrepareFile(t, dir, "package.json", `{"name":"web"}`)
	writePrepareFile(t, dir, "package-lock.json", `{"lockfileVersion":3}`)
	svc := groups.Service{
		Name: "web", Prepare: "npm ci",
		PrepareInputs: []string{"package.json", "package-lock.json"},
	}
	const config = "/repo/oberth.yaml"

	if prepareUnchanged(config, svc, dir) {
		t.Fatal("an unstamped prepare was reported unchanged")
	}
	if err := recordPrepareStamp(config, svc, dir); err != nil {
		t.Fatalf("recordPrepareStamp: %v", err)
	}
	if !prepareUnchanged(config, svc, dir) {
		t.Fatal("recorded inputs were not reported unchanged")
	}

	writePrepareFile(t, dir, "package-lock.json", `{"lockfileVersion":2}`)
	if prepareUnchanged(config, svc, dir) {
		t.Fatal("changed input contents were reported unchanged")
	}

	if err := recordPrepareStamp(config, svc, dir); err != nil {
		t.Fatalf("recordPrepareStamp: %v", err)
	}
	svc.Prepare = "npm ci --foreground-scripts"
	if prepareUnchanged(config, svc, dir) {
		t.Fatal("a changed prepare command was reported unchanged")
	}
}

func TestPrepareStampIsPerManifestAndService(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	dir := t.TempDir()
	writePrepareFile(t, dir, "package.json", `{}`)
	svc := groups.Service{Name: "web", Prepare: "npm ci", PrepareInputs: []string{"package.json"}}

	if err := recordPrepareStamp("/repo/a/oberth.yaml", svc, dir); err != nil {
		t.Fatal(err)
	}
	if prepareUnchanged("/repo/b/oberth.yaml", svc, dir) {
		t.Fatal("another checkout inherited this checkout's stamp")
	}
	other := svc
	other.Name = "api"
	if prepareUnchanged("/repo/a/oberth.yaml", other, dir) {
		t.Fatal("another service inherited this service's stamp")
	}
}

func TestPrepareMissingInputNeverSkipsOrStamps(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	dir := t.TempDir()
	svc := groups.Service{Name: "web", Prepare: "npm ci", PrepareInputs: []string{"package.json"}}

	if err := recordPrepareStamp("cfg", svc, dir); err == nil {
		t.Fatal("recording a stamp with a missing input should fail")
	}
	if prepareUnchanged("cfg", svc, dir) {
		t.Fatal("a missing input was reported unchanged")
	}
}

func TestPrepareWithoutInputsAlwaysRuns(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	dir := t.TempDir()
	svc := groups.Service{Name: "web", Prepare: "npm ci"}

	if err := recordPrepareStamp("cfg", svc, dir); err != nil {
		t.Fatalf("recordPrepareStamp without inputs should be a no-op: %v", err)
	}
	if prepareUnchanged("cfg", svc, dir) {
		t.Fatal("a step with no declared inputs was skipped")
	}
	if _, err := os.Stat(prepareStampPath("cfg", svc.Name)); !os.IsNotExist(err) {
		t.Fatalf("a stamp was written for a step with no inputs: %v", err)
	}
}

// TestSecondUpSkipsAnUnchangedPrepare is the acceptance scenario at the level
// `up` actually runs: a service is started, stopped, and started again with the
// same inputs. The second start must not run prepare; changing an input must
// make the third start run it again.
func TestSecondUpSkipsAnUnchangedPrepare(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)
	t.Setenv("BERTH_HOME", filepath.Join(dir, "berth"))
	writePrepareFile(t, dir, "package.json", `{"name":"web"}`)
	count := filepath.Join(dir, "prepare-runs")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: prepare-cache
services:
  - name: web
    prepare: sh -c 'echo run >> %s'
    prepare_inputs: [package.json]
    cmd: %s
    port: %d
`, count, serviceCmd(t), port))
	c := startDaemon(t, ctx, dir, []int{port})

	startOnce := func() []rpc.GroupsStartChunk {
		t.Helper()
		var start rpc.GroupsStartResult
		s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
		if err != nil {
			t.Fatalf("groups.start: %v", err)
		}
		defer s.Close()
		chunks, end := collect(t, s)
		for _, ch := range chunks {
			if ch.Error != "" {
				dumpLogs(t, chunks)
				t.Fatalf("start failed: %+v", ch)
			}
		}
		if len(end.Errors) != 0 {
			t.Fatalf("start end = %+v", end)
		}
		return chunks
	}
	stopOnce := func(chunks []rpc.GroupsStartChunk) {
		t.Helper()
		// probeScan attributes the listener to this test process, so the
		// daemon's kill selector would signal the test itself. Stop the child
		// the chunk names and drop its run record instead.
		for _, ch := range chunks {
			if ch.PID <= 0 {
				continue
			}
			if p, err := os.FindProcess(ch.PID); err == nil {
				_ = p.Kill()
			}
			runsreg.Default.Unregister(ch.PID)
		}
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			if !dialable(port) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the service did not stop")
	}
	runs := func() int {
		t.Helper()
		raw, err := os.ReadFile(count)
		if err != nil {
			return 0
		}
		return strings.Count(string(raw), "run")
	}

	first := startOnce()
	if got := runs(); got != 1 {
		t.Fatalf("prepare runs after the first start = %d, want 1", got)
	}
	stopOnce(first)

	second := startOnce()
	if got := runs(); got != 1 {
		t.Fatalf("prepare runs after a second unchanged start = %d, want it skipped", got)
	}
	stopOnce(second)

	writePrepareFile(t, dir, "package.json", `{"name":"web","version":"2"}`)
	third := startOnce()
	if got := runs(); got != 2 {
		t.Fatalf("prepare runs after changing an input = %d, want 2", got)
	}
	stopOnce(third)
}
