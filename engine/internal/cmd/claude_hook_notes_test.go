package cmd

// The native hook set grew a mid-turn half: PostToolUse reads the worktree's
// native Attention notes and injects one bounded finding the current turn has
// not seen. These tests pin the three things that make that safe: it is driven
// by the persisted notes (one file, no subprocess), it deduplicates by world
// revision, and a project that declares its services no longer needs every
// name repeated on the command line.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	attnengine "github.com/sheathedsharp/option-berth/engine/internal/attn/engine"
	"github.com/sheathedsharp/option-berth/engine/internal/claudehook"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
)

// hookWorld is one broken world for the project directory a test owns.
func hookWorld(root, revision string) runtimecore.World {
	return runtimecore.World{
		Schema:   runtimecore.WorldSchema,
		Revision: revision,
		Worktree: runtimecore.WorldIdentity{Group: "demo", Root: root, Branch: "feat/x"},
		Coverage: runtimecore.CoverageObservable,
		Health:   runtimecore.WorldBroken,
		Services: []runtimecore.ServiceRuntime{{Name: "api", State: runtimecore.ServiceCrashed}},
		Diagnosis: runtimecore.CausalDiagnosis{
			Healthy: false,
			RootCauses: []runtimecore.RootCause{{
				ID: "cause-api", Service: "api", Kind: runtimecore.DriftServiceCrashed,
				Severity: runtimecore.SeverityCritical, Summary: "crashed with exit code 1",
				FirstError: "Error: dial tcp token=supersecret at /Users/alice/app.env",
				Command:    "oberth logs api --once",
			}},
		},
	}
}

// publishNotes runs the deterministic engine step so a real document exists
// under the isolated BERTH_HOME.
func publishNotes(t *testing.T, home, root, revision string) {
	t.Helper()
	// The runtime service files notes under the canonical checkout root, so a
	// test must hand over the same spelling: on macOS /tmp is a symlink into
	// /private/tmp, and the two spellings hash to two worktree ids.
	root = groups.Canonical(root)
	engine := attnengine.New(attnengine.Options{Home: home})
	t.Cleanup(engine.Stop)
	engine.Observe(hookWorld(root, revision))
}

func TestClaudeHookPostToolUseDeliversFreshNoteOncePerRevision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	root := groups.Canonical(t.TempDir())
	publishNotes(t, home, root, "rev-1")

	adapter := claudehook.Adapter{Home: home, Root: root, MidTurn: claudeMidTurn(root, nil)}
	in := claudehook.Input{Event: "PostToolUse", Session: "s1", Cwd: root}

	out, err := adapter.Handle(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Specific == nil || out.Specific.Event != "PostToolUse" {
		t.Fatalf("no mid-turn context: %+v", out)
	}
	if out.Decision != "" {
		t.Fatalf("the mid-turn note blocked the turn: %+v", out)
	}
	if !strings.Contains(out.Specific.Text, "crashed with exit code 1") {
		t.Fatalf("context = %q", out.Specific.Text)
	}
	if !strings.Contains(out.Specific.Text, "oberth logs api --once") {
		t.Fatalf("context does not name the next command: %q", out.Specific.Text)
	}
	if !strings.Contains(out.Specific.Text, "first error") {
		t.Fatalf("context does not carry the first error: %q", out.Specific.Text)
	}
	if strings.Contains(out.Specific.Text, "supersecret") || strings.Contains(out.Specific.Text, "/Users/alice") {
		t.Fatalf("context leaked private text: %q", out.Specific.Text)
	}

	// The same revision is not injected twice.
	second, err := adapter.Handle(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if second.Specific != nil || second.Decision != "" {
		t.Fatalf("duplicate delivery: %+v", second)
	}

	// A new world revision is news again.
	publishNotes(t, home, root, "rev-2")
	third, err := adapter.Handle(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if third.Specific == nil {
		t.Fatal("a new revision was not delivered")
	}
}

func TestClaudeHookMidTurnRespectsTheServiceScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	root := groups.Canonical(t.TempDir())
	publishNotes(t, home, root, "rev-1")

	// A scope that does not name the broken service hears nothing.
	adapter := claudehook.Adapter{Home: home, Root: root, MidTurn: claudeMidTurn(root, []string{"web"})}
	out, err := adapter.Handle(context.Background(), claudehook.Input{Event: "PostToolUse", Session: "s1", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if out.Specific != nil {
		t.Fatalf("a note outside the task scope was delivered: %+v", out)
	}

	// The scope that names it hears it.
	scoped := claudehook.Adapter{Home: home, Root: root, MidTurn: claudeMidTurn(root, []string{"api"})}
	out, err = scoped.Handle(context.Background(), claudehook.Input{Event: "PostToolUse", Session: "s1", Cwd: root})
	if err != nil || out.Specific == nil {
		t.Fatalf("a note inside the task scope was not delivered: %+v err=%v", out, err)
	}
}

func TestAutoScopeServicesReadsTheManifestAndRefusesJunk(t *testing.T) {
	dir := t.TempDir()
	manifest := `name: demo
services:
  - name: api
    cmd: echo hi
    port: 3911
  - name: web
    cmd: echo hi
    port: 3912
`
	if err := os.WriteFile(filepath.Join(dir, "oberth.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	got := autoScopeServices(dir)
	if strings.Join(got, ",") != "api,web" {
		t.Fatalf("auto scope = %v", got)
	}

	if got := autoScopeServices(t.TempDir()); got != nil {
		t.Fatalf("a directory without a manifest invented scope: %v", got)
	}

	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "oberth.yaml"), []byte("services:\n  - port: not-a-number\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := autoScopeServices(broken); got != nil {
		t.Fatalf("an invalid manifest invented scope: %v", got)
	}
}

// The command path: a project that declares its services attaches without
// --service, and a project that declares none keeps the old refusal.
func TestClaudeHookCommandAutoScopesFromTheManifest(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	t.Setenv("BERTH_RUNTIME_SERVICE_NO_AUTOSTART", "1")
	t.Setenv("BERTH_RUNTIME_SERVICE_SOCKET", filepath.Join(t.TempDir(), "runtime.sock"))
	t.Setenv("BERTH_ATTN", "")

	withManifest := t.TempDir()
	initGit(t, withManifest)
	manifest := `name: demo
services:
  - name: api
    cmd: echo hi
    port: 3921
`
	if err := os.WriteFile(filepath.Join(withManifest, "oberth.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	out := runClaudeHookCommand(t, withManifest)
	if strings.Contains(out, "scope_required") {
		t.Fatalf("a declared project still required --service: %s", out)
	}

	withoutManifest := t.TempDir()
	initGit(t, withoutManifest)
	out = runClaudeHookCommand(t, withoutManifest)
	if !strings.Contains(out, "scope_required") {
		t.Fatalf("a project with no manifest lost its scope_required refusal: %s", out)
	}
}

func TestClaudeHookPrintConfigEmitsPostToolUse(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)
	rootCmd.SetArgs([]string{"agent", "claude-hook", "--project", dir, "--print-config"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("print-config: %v\n%s", err, out)
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				OnFailure string `json:"onFailure"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		t.Fatalf("parse print-config: %v\n%s", e, out)
	}
	entry, ok := doc.Hooks["PostToolUse"]
	if !ok {
		t.Fatalf("print-config has no PostToolUse hook: %s", out)
	}
	if len(entry) == 0 || len(entry[0].Hooks) == 0 {
		t.Fatalf("PostToolUse hook is empty: %s", out)
	}
	if entry[0].Hooks[0].OnFailure != "" {
		t.Fatalf("PostToolUse fails closed: %+v", entry[0].Hooks[0])
	}
	if hooks, ok := doc.Hooks["UserPromptSubmit"]; !ok || hooks[0].Hooks[0].OnFailure != "block" {
		t.Fatalf("the task boundary stopped failing closed: %s", out)
	}
}

func initGit(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("isolated git init: %s %v", out, err)
	}
}

func runClaudeHookCommand(t *testing.T, project string) string {
	t.Helper()
	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","cwd":` + strconvQuote(project) + `}`
	rootCmd.SetIn(strings.NewReader(payload))
	rootCmd.SetArgs([]string{"agent", "claude-hook", "--project", project,
		"--authorize", "inspect_logs", "--lease", "30s"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
	})
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("claude-hook: %v\n%s", err, out)
	}
	return out
}

func strconvQuote(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// The status document counts the notes for the current worktree without
// deriving a world: a status read stays a status read.
func TestAttnStatusReportsNativeNotes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_ATTN", "")
	project := t.TempDir()
	initGit(t, project)
	publishNotes(t, home, project, "rev-1")

	rootCmd.SetArgs([]string{"attn", "status", "--json", "--project", project})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("attn status: %v\n%s", err, out)
	}
	var doc map[string]any
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		t.Fatalf("parse attn status: %v\n%s", e, out)
	}
	notes, ok := doc["notes"].(map[string]any)
	if !ok {
		t.Fatalf("status has no notes summary: %+v", doc)
	}
	if notes["active"].(float64) != 1 || notes["health"] != string(runtimecore.WorldBroken) {
		t.Fatalf("notes summary = %+v", notes)
	}
	if doc["protocol"] != "decisions" || doc["mode"] != "assist" {
		t.Fatalf("status protocol/mode = %v/%v", doc["protocol"], doc["mode"])
	}
}

// `oberth attn notes` reads the persisted document and, with --json, prints the
// notes with their next command and safety class.
func TestAttnNotesPrintsPersistedNotes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	project := t.TempDir()
	initGit(t, project)
	publishNotes(t, home, project, "rev-1")

	rootCmd.SetArgs([]string{"attn", "notes", "--project", project, "--json"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("attn notes: %v\n%s", err, out)
	}
	var doc struct {
		Source string `json:"source"`
		Active []struct {
			Service    string `json:"service"`
			Summary    string `json:"summary"`
			Command    string `json:"command"`
			Safety     string `json:"safety"`
			FirstError string `json:"first_error"`
		} `json:"active"`
	}
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		t.Fatalf("parse attn notes: %v\n%s", e, out)
	}
	if doc.Source != "persisted" {
		t.Fatalf("source = %q, want persisted", doc.Source)
	}
	if len(doc.Active) != 1 {
		t.Fatalf("active notes = %d: %s", len(doc.Active), out)
	}
	note := doc.Active[0]
	if note.Service != "api" || note.Command != "oberth logs api --once" || note.Safety != runtimecore.SafetyReadOnly {
		t.Fatalf("note = %+v", note)
	}
	if strings.Contains(note.FirstError, "supersecret") || strings.Contains(note.FirstError, "/Users/alice") {
		t.Fatalf("first error was not redacted: %q", note.FirstError)
	}
}

// A worktree the runtime service has not persisted yet still gets its notes,
// derived live from the same facts status reads.
func TestAttnNotesDerivesLiveWhenNothingIsPersisted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_RUNTIME_SERVICE_NO_AUTOSTART", "1")
	t.Setenv("BERTH_RUNTIME_SERVICE_SOCKET", filepath.Join(t.TempDir(), "runtime.sock"))
	project := t.TempDir()
	initGit(t, project)
	manifest := `name: demo
services:
  - name: api
    cmd: echo hi
    port: 3931
`
	if err := os.WriteFile(filepath.Join(project, "oberth.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	rootCmd.SetArgs([]string{"attn", "notes", "--project", project, "--json"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("attn notes: %v\n%s", err, out)
	}
	var doc struct {
		Source string `json:"source"`
		Health string `json:"health"`
	}
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		t.Fatalf("parse attn notes: %v\n%s", e, out)
	}
	if doc.Source != "derived" {
		t.Fatalf("source = %q, want derived: %s", doc.Source, out)
	}
}
