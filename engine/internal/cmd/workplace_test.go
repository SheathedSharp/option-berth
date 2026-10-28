package cmd

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
)

// The workplace command family, driven the way the CLI drives it: the root
// command with args, a real daemon over a socket, and a real git worktree.
//
// The JSON documents are what these tests read. The human output is a view of
// the same value, and asserting on it would pin the table rather than the
// contract.

// workplaceRun runs one `oberth workplace …` line and returns what it printed
// and what it returned.
//
// The flags are reset around every call. They live on a command tree built once
// at init, so a value one test set — a `--json`, a `--id` — would otherwise
// still be there for the next one. A user's invocations are separate processes
// and never see this.
func workplaceRun(t *testing.T, jsonOut bool, args ...string) (string, error) {
	t.Helper()
	resetRouting(t)
	resetWorkplaceFlags(t)
	flag := "--json=false"
	if jsonOut {
		flag = "--json=true"
	}
	argv := append([]string{"workplace"}, args...)
	rootCmd.SetArgs(append(argv, flag))

	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	return out, err
}

func resetWorkplaceFlags(t *testing.T) {
	t.Helper()
	prevWorktree, prevJSON := workplaceWorktreeFlag, workplaceJSONFlag
	prevBlock, prevFile := workplaceRecordBlock, workplaceRecordOutputFile
	prevStarted, prevEnded, prevExit := workplaceRecordStartedAt, workplaceRecordEndedAt, workplaceRecordExitCode
	t.Cleanup(func() {
		workplaceWorktreeFlag, workplaceJSONFlag = prevWorktree, prevJSON
		workplaceRecordBlock, workplaceRecordOutputFile = prevBlock, prevFile
		workplaceRecordStartedAt, workplaceRecordEndedAt, workplaceRecordExitCode = prevStarted, prevEnded, prevExit
		rootCmd.SetArgs(nil)
	})
	workplaceWorktreeFlag, workplaceJSONFlag = "", false
	workplaceRecordBlock, workplaceRecordOutputFile = rpc.WorkplaceBlockInput{}, ""
	workplaceRecordStartedAt, workplaceRecordEndedAt, workplaceRecordExitCode = "", "", 0
}

func decodeList(t *testing.T, out string) rpc.WorkplaceListResult {
	t.Helper()
	var res rpc.WorkplaceListResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stdout is not the workplace.list document: %v\n%s", err, out)
	}
	return res
}

func decodeShow(t *testing.T, out string) rpc.WorkplaceShowResult {
	t.Helper()
	var res rpc.WorkplaceShowResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stdout is not the workplace.show document: %v\n%s", err, out)
	}
	return res
}

func TestWorkplaceLifecycleThroughTheCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket daemon harness does not apply to named pipes")
	}
	startTestDaemon(t, nil)
	repo := gitTestRepo(t)
	chdir(t, repo)

	// A worktree nobody has worked in yet has no workplace, and saying so is
	// not an error: it is the answer.
	out, err := workplaceRun(t, true, "list")
	if err != nil {
		t.Fatalf("workplace list: %v", err)
	}
	res := decodeList(t, out)
	if len(res.Workplaces) != 0 || res.Active != "" {
		t.Fatalf("a fresh worktree already has workplaces: %+v", res)
	}
	if res.Worktree != repo {
		t.Fatalf("worktree = %q, want %q", res.Worktree, repo)
	}

	// new
	if out, err = workplaceRun(t, true, "new", "spike"); err != nil {
		t.Fatalf("workplace new: %v", err)
	}
	var created rpc.WorkplaceNewResult
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("workplace new output: %v\n%s", err, out)
	}
	if created.Workplace.Name != "spike" || !created.Workplace.Active {
		t.Fatalf("new workplace = %+v", created.Workplace)
	}
	spike := created.Workplace.ID

	// A second workplace with the same name is refused rather than shadowing
	// the first.
	if _, err := workplaceRun(t, true, "new", "spike"); err == nil {
		t.Fatal("a duplicate workplace name was accepted")
	} else if !strings.Contains(err.Error(), "already has a workplace") {
		t.Fatalf("duplicate name error = %v", err)
	}

	if _, err := workplaceRun(t, true, "new", "second"); err != nil {
		t.Fatalf("workplace new second: %v", err)
	}
	out, err = workplaceRun(t, true, "list")
	if err != nil {
		t.Fatalf("workplace list: %v", err)
	}
	res = decodeList(t, out)
	if len(res.Workplaces) != 2 {
		t.Fatalf("list returned %d workplaces, want 2", len(res.Workplaces))
	}
	if res.Active == spike {
		t.Fatal("creating a workplace did not make it the one new work lands in")
	}

	// use
	if out, err = workplaceRun(t, true, "use", "spike"); err != nil {
		t.Fatalf("workplace use: %v", err)
	}
	var used rpc.WorkplaceUseResult
	if err := json.Unmarshal([]byte(out), &used); err != nil {
		t.Fatalf("workplace use output: %v\n%s", err, out)
	}
	if used.Workplace.ID != spike || !used.Workplace.Active {
		t.Fatalf("use returned %+v", used.Workplace)
	}
	if out, err = workplaceRun(t, true, "list"); err != nil {
		t.Fatalf("workplace list: %v", err)
	} else if decodeList(t, out).Active != spike {
		t.Fatal("the active workplace did not move")
	}

	// A selector that matches nothing is not_found, and one that matches two
	// things is ambiguous — never a coin flip.
	if _, err := workplaceRun(t, true, "use", "nope"); err == nil {
		t.Fatal("an unknown selector was accepted")
	} else if !strings.Contains(err.Error(), "no such workplace") {
		t.Fatalf("unknown selector error = %v", err)
	}

	// delete: the active workplace goes, and the one left takes over.
	if out, err = workplaceRun(t, true, "delete", "spike"); err != nil {
		t.Fatalf("workplace delete: %v", err)
	}
	var deleted rpc.WorkplaceDeleteResult
	if err := json.Unmarshal([]byte(out), &deleted); err != nil {
		t.Fatalf("workplace delete output: %v\n%s", err, out)
	}
	if deleted.Deleted != spike || deleted.Active == nil || deleted.Active.ID == spike {
		t.Fatalf("delete = %+v", deleted)
	}
	if deleted.Active.Name == "" {
		t.Fatalf("the workplace that took over has no name to report: %+v", deleted.Active)
	}
	out, err = workplaceRun(t, true, "list")
	if err != nil {
		t.Fatalf("workplace list: %v", err)
	}
	res = decodeList(t, out)
	if len(res.Workplaces) != 1 || res.Active != res.Workplaces[0].ID {
		t.Fatalf("after delete = %+v", res)
	}

	// Deleting the last one leaves the worktree with none, and the next
	// command that records work makes one again.
	if _, err = workplaceRun(t, true, "delete", "second"); err != nil {
		t.Fatalf("workplace delete last: %v", err)
	}
	out, err = workplaceRun(t, true, "list")
	if err != nil {
		t.Fatalf("workplace list: %v", err)
	}
	if res = decodeList(t, out); len(res.Workplaces) != 0 {
		t.Fatalf("a workplace survived the delete: %+v", res)
	}
}

func TestWorkplaceRecordAndShowThroughTheCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket daemon harness does not apply to named pipes")
	}
	startTestDaemon(t, nil)
	repo := gitTestRepo(t)
	chdir(t, repo)

	// Recording into a worktree with no workplace creates the default one:
	// recording work is the thing a workplace exists to hold.
	out, err := workplaceRun(t, true, "record",
		"--kind", "shell", "--id", "b1", "--input", "npm test", "--status", "running")
	if err != nil {
		t.Fatalf("workplace record: %v", err)
	}
	var recorded rpc.WorkplaceRecordResult
	if err := json.Unmarshal([]byte(out), &recorded); err != nil {
		t.Fatalf("record output: %v\n%s", err, out)
	}
	if recorded.Workplace.Name != "default" || recorded.Block.Status != "running" {
		t.Fatalf("record = %+v", recorded)
	}
	if recorded.Block.Source != "reported" {
		t.Fatalf("an explicit report was recorded as source %q, want reported", recorded.Block.Source)
	}

	// Reporting the same id again updates one block instead of adding one, and
	// an outcome is not undone by a late start.
	if _, err := workplaceRun(t, true, "record",
		"--kind", "shell", "--id", "b1", "--status", "completed", "--exit-code", "0"); err != nil {
		t.Fatalf("workplace record end: %v", err)
	}
	if _, err := workplaceRun(t, true, "record",
		"--kind", "shell", "--id", "b1", "--input", "npm test", "--status", "running"); err != nil {
		t.Fatalf("workplace record late start: %v", err)
	}
	if _, err := workplaceRun(t, true, "record",
		"--kind", "agent", "--id", "b2", "--provider", "codex", "--input", "fix the build",
		"--status", "failed"); err != nil {
		t.Fatalf("workplace record agent: %v", err)
	}

	out, err = workplaceRun(t, true, "show")
	if err != nil {
		t.Fatalf("workplace show: %v", err)
	}
	shown := decodeShow(t, out)
	if len(shown.Blocks) != 2 {
		t.Fatalf("show returned %d blocks, want 2 (the two observations of b1 are one block)", len(shown.Blocks))
	}
	first := shown.Blocks[0]
	if first.ID != "b1" || first.Input != "npm test" || first.Status != "completed" {
		t.Fatalf("the first block = %+v", first)
	}
	if first.ExitCode == nil || *first.ExitCode != 0 || first.EndedAt == "" {
		t.Fatalf("the outcome did not survive: %+v", first)
	}
	if second := shown.Blocks[1]; second.Kind != "agent" || second.Provider != "codex" || second.Status != "failed" {
		t.Fatalf("the agent block = %+v", second)
	}
	if shown.Truncated {
		t.Fatal("a two-block record reported itself truncated")
	}

	// A limit keeps the newest and says the list is not the whole record.
	out, err = workplaceRun(t, true, "show", "--limit", "1")
	if err != nil {
		t.Fatalf("workplace show --limit: %v", err)
	}
	capped := decodeShow(t, out)
	if len(capped.Blocks) != 1 || !capped.Truncated || capped.Blocks[0].ID != "b2" {
		t.Fatalf("capped show = %+v", capped)
	}

	// A report the record cannot store is refused rather than half-written.
	if _, err := workplaceRun(t, true, "record", "--kind", "browser"); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	if _, err := workplaceRun(t, true, "record", "--input", "no kind"); err == nil {
		t.Fatal("a record with no kind was accepted")
	}
}

// TestWorkplaceReadsWorkWithoutADaemon covers the read path the CLI keeps for a
// machine where nothing is running: "what is recorded here" is a question the
// database answers on its own.
func TestWorkplaceReadsWorkWithoutADaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket daemon harness does not apply to named pipes")
	}
	startTestDaemon(t, nil)
	repo := gitTestRepo(t)
	chdir(t, repo)

	if _, err := workplaceRun(t, true, "record", "--kind", "shell", "--input", "ls", "--status", "completed"); err != nil {
		t.Fatalf("workplace record: %v", err)
	}
	if _, err := workplaceRun(t, true, "new", "spike"); err != nil {
		t.Fatalf("workplace new: %v", err)
	}

	// With the daemon unreachable, the same reads still answer from the file.
	noDaemonReachable(t)
	out, err := workplaceRun(t, true, "list")
	if err != nil {
		t.Fatalf("workplace list without a daemon: %v", err)
	}
	res := decodeList(t, out)
	if len(res.Workplaces) != 2 || res.Active == "" {
		t.Fatalf("the direct read lost the record: %+v", res)
	}
	out, err = workplaceRun(t, true, "show", "default")
	if err != nil {
		t.Fatalf("workplace show without a daemon: %v", err)
	}
	if shown := decodeShow(t, out); len(shown.Blocks) != 1 || shown.Blocks[0].Input != "ls" {
		t.Fatalf("the direct read lost the blocks: %+v", shown)
	}

	// A write has nowhere else to go: without a daemon it is refused rather
	// than written by a second writer.
	if _, err := workplaceRun(t, true, "new", "another"); err == nil {
		t.Fatal("a write was accepted with no daemon to own it")
	}
}

// TestStartRecordsIntoTheWorkplace is the engine recording its own work: a
// command run through `oberth start` appears in the worktree's workplace with
// the run identity and the log path that make it checkable afterwards.
func TestStartRecordsIntoTheWorkplace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket daemon harness does not apply to named pipes")
	}
	if _, err := os.Stat("/bin/echo"); err != nil {
		t.Skip("/bin/echo is not available")
	}
	startTestDaemon(t, nil)
	repo := gitTestRepo(t)
	chdir(t, repo)

	prev := startGroup
	startGroup = ""
	t.Cleanup(func() { startGroup = prev })

	// The command's own output went to the terminal this test captures; what
	// this test is about is the record it left behind.
	if _, err := runCommandLine(t, "start", "--", "/bin/echo", "hello"); err != nil {
		t.Fatalf("oberth start: %v", err)
	}

	shown, err := workplaceRun(t, true, "show")
	if err != nil {
		t.Fatalf("workplace show: %v", err)
	}
	res := decodeShow(t, shown)
	if len(res.Blocks) != 1 {
		t.Fatalf("`oberth start` recorded %d blocks, want 1: %+v", len(res.Blocks), res.Blocks)
	}
	block := res.Blocks[0]
	if block.Kind != "shell" || block.Source != "engine" {
		t.Fatalf("the block is not an engine shell block: %+v", block)
	}
	if block.Input != "/bin/echo hello" {
		t.Fatalf("input = %q, want the command line", block.Input)
	}
	if block.Status != "completed" || block.ExitCode == nil || *block.ExitCode != 0 {
		t.Fatalf("the block has no outcome: %+v", block)
	}
	if block.RunID == "" || block.PID == 0 {
		t.Fatalf("the block does not point at the run it recorded: %+v", block)
	}
	// The command's output went to this terminal, not to a log file, so the
	// block has no log path to point at — which is why it carries the run id
	// instead.
	if block.LogPath != "" {
		t.Fatalf("an attached run reported a log file: %q", block.LogPath)
	}
}

// runCommandLine runs one non-workplace `oberth …` line and returns its stdout.
func runCommandLine(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetRouting(t)
	resetWorkplaceFlags(t)
	rootCmd.SetArgs(args)
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	return out, err
}

// TestAgentStreamRecordsItsTurns is the other half of the engine recording its
// own work: every turn the structured bridge runs becomes one block, opened
// when the turn starts and closed with the outcome the provider reported.
func TestAgentStreamRecordsItsTurns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket daemon harness does not apply to named pipes")
	}
	startTestDaemon(t, nil)
	repo := gitTestRepo(t)
	chdir(t, repo)
	t.Setenv("BERTH_AGENT_STREAM_FAKE", "codex")

	// One turn, then end of input: the fake driver answers a turn with
	// turn.started and turn.completed, the way a real provider does.
	rootCmd.SetIn(strings.NewReader(`{"v":1,"type":"turn.start","text":"fix the failing test"}` + "\n"))
	t.Cleanup(func() { rootCmd.SetIn(os.Stdin) })

	if _, err := runCommandLine(t, "agent", "stream", "codex", "--worktree", repo); err != nil {
		t.Fatalf("agent stream: %v", err)
	}

	shown, err := workplaceRun(t, true, "show")
	if err != nil {
		t.Fatalf("workplace show: %v", err)
	}
	res := decodeShow(t, shown)
	if len(res.Blocks) != 1 {
		t.Fatalf("one turn recorded %d blocks: %+v", len(res.Blocks), res.Blocks)
	}
	block := res.Blocks[0]
	if block.Kind != "agent" || block.Source != "engine" || block.Provider != "codex" {
		t.Fatalf("the block is not an engine agent block: %+v", block)
	}
	if block.Input != "fix the failing test" {
		t.Fatalf("input = %q, want the turn's prompt", block.Input)
	}
	if block.Status != "completed" || block.EndedAt == "" {
		t.Fatalf("the turn has no outcome: %+v", block)
	}
}
