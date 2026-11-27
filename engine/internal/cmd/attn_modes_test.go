package cmd

// The two verbs that changed shape: `attn on` can store the autonomy in the
// same consent flip, and `attn notes --all` shows the resolved history. Both
// are one flag, and both have to keep the files they write valid for every
// configuration that existed before the flag.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	attnengine "github.com/sheathedsharp/option-berth/engine/internal/attn/engine"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/claudehook"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
)

// newNotesEngine runs only the deterministic step: notes need no workers and
// no provider, which is the property these tests lean on.
func newNotesEngine(home string) *attnengine.Engine {
	return attnengine.New(attnengine.Options{Home: home})
}

// healthyWorldForTest is the same worktree with nothing wrong, in the canonical
// spelling the notes are filed under.
func healthyWorldForTest(root, revision string) runtimecore.World {
	world := hookWorld(groups.Canonical(root), revision)
	world.Health = runtimecore.WorldHealthy
	world.Diagnosis = runtimecore.CausalDiagnosis{Healthy: true, Summary: "all declared services are ready"}
	for i := range world.Services {
		world.Services[i].State = runtimecore.ServiceReady
		world.Services[i].Drifts = nil
	}
	return world
}

func resetAttnOnModeFlag(t *testing.T) {
	t.Helper()
	savedMode, savedJSON := attnOnModeFlag, attnJSONFlag
	savedNotesAll := attnNotesAllFlag
	t.Cleanup(func() {
		attnOnModeFlag, attnJSONFlag = savedMode, savedJSON
		attnNotesAllFlag = savedNotesAll
		for _, flag := range []string{"mode"} {
			if found := attnOnCmd.Flags().Lookup(flag); found != nil {
				found.Changed = false
			}
		}
		if found := attnNotesCmd.Flags().Lookup("all"); found != nil {
			found.Changed = false
		}
		rootCmd.SetArgs(nil)
	})
	attnJSONFlag = true
}

func TestAttnOnStoresModeWithConsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_ATTN", "")
	t.Setenv("BERTH_ATTN_ENDPOINT", "")
	t.Setenv("BERTH_ATTN_MODEL", "")
	t.Setenv("BERTH_ATTN_KEY", "")
	t.Setenv("BERTH_ATTN_MODE", "")
	resetAttnOnModeFlag(t)

	path := model.LocalConfigPath()
	if err := model.WriteSettings(path, model.LocalConfig{
		Schema: model.LocalConfigSchema, Endpoint: "https://assess.example/decisions",
		Model: "test/model", APIKey: "secret-key",
	}); err != nil {
		t.Fatal(err)
	}

	rootCmd.SetArgs([]string{"attn", "on", "--mode", "reconcile"})
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("attn on --mode: %v\n%s", err, out)
	}
	cfg, present, loadErr := model.LoadLocalConfig()
	if loadErr != nil || !present {
		t.Fatalf("settings = %+v present=%v err=%v", cfg, present, loadErr)
	}
	if !cfg.Enabled || cfg.Mode != "reconcile" {
		t.Fatalf("stored settings = %+v", cfg)
	}
	if cfg.APIKey != "secret-key" {
		t.Fatal("turning the assessment on dropped the stored key")
	}
	// The flag is validated before anything is written.
	saved := cfg.Mode
	rootCmd.SetArgs([]string{"attn", "on", "--mode", "autopilot"})
	out = captureStdout(t, func() { err = rootCmd.Execute() })
	if err == nil {
		t.Fatalf("an unknown mode was accepted: %s", out)
	}
	after, _, _ := model.LoadLocalConfig()
	if after.Mode != saved {
		t.Fatalf("a rejected mode still rewrote the file: %+v", after)
	}
}

func TestAttnNotesAllShowsResolvedHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	project := t.TempDir()
	initGit(t, project)
	publishNotes(t, home, project, "rev-1")
	// A healthy world resolves the note; the history stays for one look.
	engine := newNotesEngine(home)
	engine.Observe(healthyWorldForTest(project, "rev-2"))
	engine.Stop()

	resetAttnOnModeFlag(t)
	rootCmd.SetArgs([]string{"attn", "notes", "--project", project, "--all"})
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("attn notes --all: %v\n%s", err, out)
	}
	var doc struct {
		Active   []map[string]any `json:"active"`
		Resolved []map[string]any `json:"resolved"`
	}
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		t.Fatalf("parse attn notes --all: %v\n%s", e, out)
	}
	if len(doc.Active) != 0 {
		t.Fatalf("active notes after recovery = %+v", doc.Active)
	}
	if len(doc.Resolved) != 1 {
		t.Fatalf("resolved history = %+v", doc.Resolved)
	}
	if doc.Resolved[0]["status"] != string(attnengine.NoteResolved) {
		t.Fatalf("resolved note = %+v", doc.Resolved[0])
	}
}

// Without --all the same document keeps its history but does not print it.
func TestAttnNotesHidesResolvedHistoryWithoutAll(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	project := t.TempDir()
	initGit(t, project)
	publishNotes(t, home, project, "rev-1")
	engine := newNotesEngine(home)
	engine.Observe(healthyWorldForTest(project, "rev-2"))
	engine.Stop()

	resetAttnOnModeFlag(t)
	rootCmd.SetArgs([]string{"attn", "notes", "--project", project, "--json"})
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("attn notes: %v\n%s", err, out)
	}
	if strings.Contains(out, `"resolved"`) {
		t.Fatalf("resolved history leaked without --all: %s", out)
	}
}

// A tool call that arrives before the runtime service has persisted anything
// says nothing: an empty response, not a guess and not a failure.
func TestClaudeHookMidTurnWithoutNotesIsSilent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	root := t.TempDir()
	adapter := claudehook.Adapter{Home: home, Root: root, MidTurn: claudeMidTurn(root, nil)}
	out, err := adapter.Handle(context.Background(), claudehook.Input{Event: "PostToolUse", Session: "s1", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if out.Specific != nil || out.Decision != "" || out.Message != "" {
		t.Fatalf("response without notes = %+v", out)
	}
	if _, statErr := os.Stat(filepath.Join(home, "attention", "notes")); statErr == nil {
		t.Fatal("a read created the notes directory")
	}
}

func TestAttnStatusSummaryIncludesWorldHealth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	project := t.TempDir()
	initGit(t, project)
	publishNotes(t, home, project, "rev-1")

	summary := attnNoteSummary(project)
	if summary == nil {
		t.Fatal("expected persisted note summary")
	}
	line := attnNotesSummaryLine(summary)
	if !strings.Contains(line, "world broken") {
		t.Fatalf("summary line = %q, want world broken", line)
	}
}
