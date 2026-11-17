package cmd

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"github.com/spf13/cobra"
)

// The refusal hint is the recorded run as a start line: without cwd, group,
// name and port the caller would have to remember all four after the process
// died.
func TestAdHocStartCommandUsesTheRecordedRun(t *testing.T) {
	port := 8082
	rec := rpc.RunRecord{
		Group: "ai-employee-server", Name: "finance-tax-web", Cwd: "/work/web",
		Cmd: "yarn dev", PortHint: &port, Status: "exited",
	}
	want := "cd /work/web && oberth start -d --group ai-employee-server --name finance-tax-web --port 8082 -- yarn dev"
	if got := adHocStartCommand(rec); got != want {
		t.Fatalf("start line = %q, want %q", got, want)
	}

	want = "oberth start -d --group g --name n"
	if got := adHocStartCommand(rpc.RunRecord{Group: "g", Name: "n"}); got != want {
		t.Fatalf("bare start line = %q, want %q", got, want)
	}
}

func TestAdHocRestartHintPicksTheNewestMatchingRun(t *testing.T) {
	older := rpc.RunRecord{ID: "old", Group: "g", Name: "web", Cwd: "/old",
		Cmd: "old cmd", StartedAt: "2026-11-13T09:00:00Z", Status: "exited"}
	newer := rpc.RunRecord{ID: "new", Group: "g", Name: "web", Cwd: "/new",
		Cmd: "new cmd", StartedAt: "2026-11-13T10:00:00Z", Status: "exited"}
	otherGroup := rpc.RunRecord{ID: "other", Group: "elsewhere", Name: "web", Cwd: "/other",
		Cmd: "other cmd", StartedAt: "2026-11-13T11:00:00Z", Status: "exited"}

	hint := adHocRestartHint([]rpc.RunRecord{older, newer, otherGroup}, []string{"web"}, "g")
	if !strings.Contains(hint, "/new") || strings.Contains(hint, "/old") || strings.Contains(hint, "/other") {
		t.Fatalf("hint = %q, want the newest run in the caller's group", hint)
	}

	live := newer
	live.Status = "running"
	hint = adHocRestartHint([]rpc.RunRecord{live}, []string{"web"}, "g")
	if !strings.HasPrefix(hint, "this ad-hoc run is still running;") {
		t.Fatalf("live hint = %q, want the stop-first wording", hint)
	}

	if got := adHocRestartHint([]rpc.RunRecord{newer}, []string{"missing"}, "g"); got != "" {
		t.Fatalf("unknown name hint = %q, want none", got)
	}
}

// restartTestCommand is the cobra command `restart` runs under: RunE reads the
// context, which the offline ledger read needs.
func restartTestCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

// withRestartFlags installs one flag combination for the duration of a test.
// `restart` binds pflag values straight into these package variables, so a test
// that runs it leaves them set for whatever runs next.
func withRestartFlags(t *testing.T, only []string) {
	t.Helper()
	oldOnly, oldForce := restartOnly, restartForce
	oldJSON, oldOutside := restartJSON, restartAllowOutsideHome
	t.Cleanup(func() {
		restartOnly, restartForce = oldOnly, oldForce
		restartJSON, restartAllowOutsideHome = oldJSON, oldOutside
	})
	restartOnly, restartForce, restartJSON, restartAllowOutsideHome = only, false, false, false
}

// withoutManifest moves into a fresh directory with no oberth.yaml and an empty
// run ledger, so the restart below has nothing to resolve a project from.
func withoutManifest(t *testing.T) string {
	t.Helper()
	t.Setenv("BERTH_HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

// notFoundRestart runs `restart --only <name>` with no manifest and returns the
// coded refusal it answered with.
func notFoundRestart(t *testing.T) *cliErr {
	t.Helper()
	err := restartRun(restartTestCommand(), nil)
	var coded *cliErr
	if !errors.As(err, &coded) || coded.Code != "not_found" {
		t.Fatalf("error = %v, want a not_found cliErr", err)
	}
	return coded
}

// A worktree with no manifest still has a run ledger. When the run named by
// --only is recorded there, the refusal explains it with the same start line the
// manifest-present path offers, instead of failing as an internal error.
func TestRestartWithoutManifestOffersTheRecordedLiveRun(t *testing.T) {
	dir := withoutManifest(t)
	if err := runs.Add(runs.Entry{
		PID: os.Getpid(), ID: "run-1", Group: "demo", Name: "web",
		Cmd: "yarn dev", Cwd: dir, PortHint: 8082,
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	withRestartFlags(t, []string{"web"})

	coded := notFoundRestart(t)
	want := "this ad-hoc run is still running; stop it first, then: cd " + dir +
		" && oberth start -d --group demo --name web --port 8082 -- yarn dev"
	if coded.Hint != want {
		t.Fatalf("hint = %q, want %q", coded.Hint, want)
	}
	if !strings.Contains(coded.Msg, "no oberth.yaml at or above") {
		t.Fatalf("message = %q, want it to name the missing manifest", coded.Msg)
	}
}

// The ended runs live in the store, not the runs mirror; a worktree with no
// manifest reads that half offline and offers the same start line.
func TestRestartWithoutManifestOffersTheRecordedExitedRun(t *testing.T) {
	dir := withoutManifest(t)
	exited := time.Now().UTC()
	seedRunExit(t, store.RunExitRow{
		ID: "run-2", PID: 4242, Group: "demo", Name: "web", Cmd: "yarn dev",
		Cwd: dir, PortHint: 8082, StartedAt: exited.Add(-time.Minute),
		ExitedAt: exited, Reason: "crashed",
	})
	withRestartFlags(t, []string{"web"})

	coded := notFoundRestart(t)
	want := "to restart this ad-hoc run: cd " + dir +
		" && oberth start -d --group demo --name web --port 8082 -- yarn dev"
	if coded.Hint != want {
		t.Fatalf("hint = %q, want %q", coded.Hint, want)
	}
}

// A name the ledger does not know is not an explanation: the caller gets the
// manifest refusal, which is what tells them how to make the project usable.
func TestRestartWithoutManifestKeepsTheManifestHintForAnUnknownName(t *testing.T) {
	dir := withoutManifest(t)
	if err := runs.Add(runs.Entry{
		PID: os.Getpid(), ID: "run-1", Group: "demo", Name: "web", Cmd: "yarn dev", Cwd: dir,
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	withRestartFlags(t, []string{"missing"})

	coded := notFoundRestart(t)
	if !strings.Contains(coded.Hint, "oberth init") {
		t.Fatalf("hint = %q, want the manifest hint", coded.Hint)
	}
	if strings.Contains(coded.Hint, "oberth start") {
		t.Fatalf("hint = %q, want no start line for a name the ledger does not know", coded.Hint)
	}
	if code, _, _ := describe(coded); code != "not_found" {
		t.Fatalf("code = %q, want not_found, never internal", code)
	}
}

// seedRunExit writes one ended run into the store the offline read opens.
func seedRunExit(t *testing.T, row store.RunExitRow) {
	t.Helper()
	db, err := store.Open(store.Path())
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	if err := db.AppendRunExit(row); err != nil {
		_ = db.Close()
		t.Fatalf("recording the ended run: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}
}
