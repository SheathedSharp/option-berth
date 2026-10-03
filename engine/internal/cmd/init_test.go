package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/config"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/paths"
)

// clearInitFlags zeroes action flags and returns the restore, so each test gets a clean slate.
func clearInitFlags() func() {
	prev := struct {
		dry, json, draftDry, draftJSON, draftProgress, replace, adoptMerge, adoptDry, adoptDiff, adoptJSON bool
	}{initDryRunFlag, initJSONFlag, initDraftDryRunFlag, initDraftJSONFlag, initDraftProgressFlag, initAdoptReplaceFlag, initAdoptMergeFlag, initAdoptDryRunFlag, initAdoptDiffFlag, initAdoptJSONFlag}
	initDryRunFlag, initJSONFlag = false, false
	initDraftDryRunFlag, initDraftJSONFlag, initDraftProgressFlag = false, false, false
	initAdoptReplaceFlag, initAdoptMergeFlag = false, false
	initAdoptDryRunFlag, initAdoptDiffFlag, initAdoptJSONFlag = false, false, false
	return func() {
		initDryRunFlag, initJSONFlag = prev.dry, prev.json
		initDraftDryRunFlag, initDraftJSONFlag, initDraftProgressFlag = prev.draftDry, prev.draftJSON, prev.draftProgress
		initAdoptReplaceFlag, initAdoptMergeFlag = prev.replace, prev.adoptMerge
		initAdoptDryRunFlag, initAdoptDiffFlag, initAdoptJSONFlag = prev.adoptDry, prev.adoptDiff, prev.adoptJSON
	}
}

// fakeAgentCLI puts an executable `claude` stub on PATH ahead of whatever the
// machine has, so a named run resolves to the stub while the rest of PATH
// stays intact for the port scan behind every init.
func fakeAgentCLI(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// projectDir is a private project directory whose base name is the test's.
// The drafts directory is shared across the tests in this package, and the
// group name is the directory's base — two tests both standing in a temp dir
// called `001` would resolve to the same draft.
func projectDir(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// validAgentAnswer is what a stub agent prints: the result envelope claude
// prints under --output-format json, carrying the schema-shaped answer in
// structured_output. A little prose in front of it is tolerated, as it is on a
// real run.
const validAgentAnswer = `{"is_error":false,"result":"done","structured_output":` +
	`{"name":"proj","services":[{"name":"api","cmd":"echo hi","port":3999,"why":"package.json scripts.dev"}]}}`

// draftStub is a stub body that produces that reply.
const draftStub = "cat <<'OUT'\nHere is the draft.\n\n" + validAgentAnswer + "\nOUT\n"

// The two-step in full: draft archives a proposal and adopt writes the manifest.
func TestInitDraftThenAdopt(t *testing.T) {
	t.Cleanup(clearInitFlags())
	proj := projectDir(t)
	chdir(t, proj)
	fakeAgentCLI(t, draftStub)

	if err := initDraftRun(initDraftCmd, []string{"claude"}); err != nil {
		t.Fatalf("init draft: %v", err)
	}
	manifest := filepath.Join(proj, groups.ConfigName)
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatalf("init draft wrote the manifest: %v", err)
	}
	draftPath := paths.Draft(filepath.Base(proj))
	if _, err := os.Stat(draftPath); err != nil {
		t.Fatalf("no draft archived at %s: %v", draftPath, err)
	}

	if err := initAdoptRun(initAdoptCmd, nil); err != nil {
		t.Fatalf("init adopt: %v", err)
	}
	out, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("the manifest was not written: %v", err)
	}
	cfg, err := groups.Parse(manifest, out)
	if err != nil {
		t.Fatalf("the adopted manifest does not parse: %v", err)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].Name != "api" ||
		cfg.Services[0].Cmd != "echo hi" || cfg.Services[0].Port != 3999 {
		t.Fatalf("adopted services = %+v", cfg.Services)
	}
	if !strings.Contains(string(out), "# Drafted by") {
		t.Errorf("the adopted file lost its provenance:\n%s", out)
	}

	if err := initRun(initCmd, nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a second adopt = %v, want the already-exists refusal", err)
	}
}

// Draft JSON is one documented document on stdout, with the draft in it.
func TestInitDraftJSONShape(t *testing.T) {
	t.Cleanup(clearInitFlags())
	proj := projectDir(t)
	chdir(t, proj)
	fakeAgentCLI(t, draftStub)

	initDraftDryRunFlag, initDraftJSONFlag = false, true
	var err error
	out := captureStdout(t, func() { err = initDraftRun(initDraftCmd, []string{"claude"}) })
	if err != nil {
		t.Fatalf("init draft --json: %v", err)
	}
	var got initResult
	if jsonErr := json.Unmarshal([]byte(out), &got); jsonErr != nil {
		t.Fatalf("stdout is not one JSON value: %v\n%s", jsonErr, out)
	}
	if got.Agent != "claude" || got.Draft == "" || got.Written || got.Services != 1 {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(got.YAML, "port: 3999") {
		t.Fatalf("the draft is not in the document: %+v", got)
	}
}

// Progress mode is an NDJSON stream: early events make a slow local agent
// visible, and the final event carries the same validated YAML as --json.
func TestInitDraftProgressJSONL(t *testing.T) {
	t.Cleanup(clearInitFlags())
	proj := projectDir(t)
	chdir(t, proj)
	fakeAgentCLI(t, draftStub)

	initDraftDryRunFlag, initDraftJSONFlag, initDraftProgressFlag = true, true, true
	var err error
	out := captureStdout(t, func() { err = initDraftRun(initDraftCmd, []string{"claude"}) })
	if err != nil {
		t.Fatalf("init draft --progress: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("progress output has %d lines, want preparation, agent, and done:\n%s", len(lines), out)
	}
	var done struct {
		Type string `json:"type"`
		YAML string `json:"yaml"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &done); err != nil {
		t.Fatalf("last progress line is not JSON: %v\n%s", err, lines[len(lines)-1])
	}
	if done.Type != "done" || !strings.Contains(done.YAML, "name: proj") {
		t.Fatalf("done = %+v, want validated YAML", done)
	}
}

func TestInitDraftSubcommandUsesPositionalAgent(t *testing.T) {
	t.Cleanup(clearInitFlags())
	proj := projectDir(t)
	chdir(t, proj)
	fakeAgentCLI(t, draftStub)
	initDraftDryRunFlag, initDraftJSONFlag = true, true
	var err error
	out := captureStdout(t, func() { err = initDraftRun(initDraftCmd, []string{"claude"}) })
	if err != nil {
		t.Fatalf("init draft: %v", err)
	}
	var got initResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if got.Agent != "claude" || got.Draft != "" || got.Written {
		t.Fatalf("draft result = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(proj, groups.ConfigName)); !os.IsNotExist(err) {
		t.Fatalf("init draft wrote the manifest: %v", err)
	}
}

func TestInitAdoptSubcommandMapsReplace(t *testing.T) {
	t.Cleanup(clearInitFlags())
	proj := projectDir(t)
	chdir(t, proj)
	target := filepath.Join(proj, groups.ConfigName)
	if err := os.WriteFile(target, []byte("name: old\nservices: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	draftPath := filepath.Join(t.TempDir(), "draft.yaml")
	if err := os.WriteFile(draftPath, []byte("name: new\nservices: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initAdoptReplaceFlag, initAdoptJSONFlag = true, true
	var err error
	out := captureStdout(t, func() { err = initAdoptRun(initAdoptCmd, []string{draftPath}) })
	if err != nil {
		t.Fatalf("init adopt --replace: %v", err)
	}
	var got initResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if !got.Written || got.AdoptedFrom != draftPath {
		t.Fatalf("adopt result = %+v", got)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: new") || strings.Contains(string(data), "name: old") {
		t.Fatalf("replacement did not take effect: %s", data)
	}
}

// A dry-run is the safe first pass for a new project: it returns the same
// validated YAML, but leaves both the project and the draft archive untouched.
func TestInitDraftDryRunDoesNotArchive(t *testing.T) {
	t.Cleanup(clearInitFlags())
	proj := projectDir(t)
	chdir(t, proj)
	fakeAgentCLI(t, draftStub)

	initDraftDryRunFlag = true
	if err := initDraftRun(initDraftCmd, []string{"claude"}); err != nil {
		t.Fatalf("init draft --dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, groups.ConfigName)); !os.IsNotExist(err) {
		t.Fatalf("--dry-run wrote the manifest: %v", err)
	}
	if _, err := os.Stat(paths.Draft(filepath.Base(proj))); !os.IsNotExist(err) {
		t.Fatalf("--dry-run archived a draft: %v", err)
	}
}

// agent.args reaches the CLI, before the prompt.
func TestInitDraftPassesConfiguredArgs(t *testing.T) {
	t.Cleanup(clearInitFlags())
	prevConfig := loadedConfig
	loadedConfig = &config.Config{}
	loadedConfig.Agent.Args = []string{"--allowedTools", "Bash"}
	t.Cleanup(func() { loadedConfig = prevConfig })

	proj := projectDir(t)
	chdir(t, proj)
	fakeAgentCLI(t, "echo \"$@\" > args.txt\n"+draftStub)

	if err := initDraftRun(initDraftCmd, []string{"claude"}); err != nil {
		t.Fatalf("init draft: %v", err)
	}
	args, err := os.ReadFile(filepath.Join(proj, "args.txt"))
	if err != nil {
		t.Fatalf("the stub got no arguments: %v", err)
	}
	got := string(args)
	if !strings.Contains(got, "--allowedTools Bash") || !strings.Contains(got, "# This project") {
		t.Errorf("arguments = %q, want agent.args and then the brief", got)
	}
	// agent.args comes after the flags option-berth adds and before the prompt:
	// a person's explicit argument wins over the panel's, and the prompt is
	// last because it is the one argument that could look like a flag.
	if extra, brief := strings.Index(got, "--allowedTools Bash"), strings.Index(got, "# This project"); extra > brief {
		t.Errorf("agent.args should precede the brief: %q", got)
	}
}

func TestInitDraftRejectsABadDraft(t *testing.T) {
	t.Cleanup(clearInitFlags())
	chdir(t, projectDir(t))
	fakeAgentCLI(t, "cat <<'OUT'\n"+
		`{"is_error":false,"result":"done","structured_output":{"name":"proj","services":[{"name":"api","cmd":"echo hi","port":70000,"why":"x"}]}}`+
		"\nOUT\n")

	err := initDraftRun(initDraftCmd, []string{"claude"})
	if err == nil {
		t.Fatal("a draft with a bad port was accepted")
	}
	if code, _, _ := describe(err); code != "invalid_config" {
		t.Fatalf("code = %s (%v)", code, err)
	}
	if !strings.Contains(err.Error(), "70000") {
		t.Errorf("error should name the bad value: %v", err)
	}
}

func TestInitDraftWithoutAnAnswer(t *testing.T) {
	t.Cleanup(clearInitFlags())
	chdir(t, projectDir(t))
	fakeAgentCLI(t, "echo 'sorry, found nothing worth declaring'\n")

	err := initDraftRun(initDraftCmd, []string{"claude"})
	if err == nil {
		t.Fatal("a reply with no answer document was accepted")
	}
	code, _, hint := describe(err)
	if code != "invalid_config" || !strings.Contains(hint, "draft schema") {
		t.Fatalf("code = %s, hint = %s (%v)", code, hint, err)
	}
}

func TestInitDraftReportsAFailedAgent(t *testing.T) {
	t.Cleanup(clearInitFlags())
	chdir(t, projectDir(t))
	fakeAgentCLI(t, "exit 3\n")

	err := initDraftRun(initDraftCmd, []string{"claude"})
	if err == nil {
		t.Fatal("a failing agent was reported as success")
	}
	code, msg, _ := describe(err)
	if code != "internal" || !strings.Contains(msg, "exit code 3") {
		t.Fatalf("code = %s, message = %s", code, msg)
	}
}

func TestInitAdoptWithoutADraft(t *testing.T) {
	t.Cleanup(clearInitFlags())
	chdir(t, projectDir(t))
	err := initAdoptRun(initAdoptCmd, nil)
	if err == nil {
		t.Fatal("adopting a draft that does not exist succeeded")
	}
	code, msg, hint := describe(err)
	if code != "not_found" || !strings.Contains(msg, "no draft") || !strings.Contains(hint, "init draft") {
		t.Fatalf("code = %s, message = %s, hint = %s", code, msg, hint)
	}
}

func TestAdoptionDiffReviewsExactBytesWithoutWriting(t *testing.T) {
	for _, merge := range []bool{false, true} {
		t.Run(map[bool]string{false: "replace", true: "merge"}[merge], func(t *testing.T) {
			t.Cleanup(clearInitFlags())
			dir := projectDir(t)
			const current = "# keep the worker\nname: demo\nservices:\n  - name: worker\n    cmd: python3 worker.py\n"
			const proposed = "# why: server.py\nname: demo\nservices:\n  - name: api\n    cmd: python3 server.py\n    port: 18090\n"
			target := filepath.Join(dir, groups.ConfigName)
			source := filepath.Join(t.TempDir(), "draft.yaml")
			if err := os.WriteFile(target, []byte(current), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte(proposed), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			out := captureStdout(t, func() { err = initAdoptDraft(dir, source, !merge, merge, true, true, true) })
			if err != nil {
				t.Fatal(err)
			}
			var doc initResult
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Written || doc.Diff == nil || !strings.Contains(*doc.Diff, "+    cmd: python3 server.py") {
				t.Fatalf("preview = %+v", doc)
			}
			if strings.Contains(doc.YAML, "cmd: python3 worker.py") != merge {
				t.Fatalf("merge=%v: %s", merge, doc.YAML)
			}
			for path, want := range map[string]string{target: current, source: proposed} {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != want {
					t.Fatalf("preview changed %s: %v", path, err)
				}
			}
		})
	}
}

func TestResolveAgent(t *testing.T) {
	t.Cleanup(clearInitFlags())
	prevConfig := loadedConfig
	loadedConfig = &config.Config{}
	t.Cleanup(func() { loadedConfig = prevConfig })

	stub := func(names ...string) string {
		dir := t.TempDir()
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	t.Run("named", func(t *testing.T) {
		t.Setenv("PATH", stub("claude", "codex"))
		cli, err := resolveAgent("claude")
		if err != nil || cli.Name != "claude" {
			t.Fatalf("resolveAgent = %q, %v", cli.Name, err)
		}
	})

	t.Run("named but not installed", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := resolveAgent("codex")
		code, _, hint := describe(err)
		if code != "not_found" || !strings.Contains(hint, "agent") {
			t.Fatalf("code = %s, hint = %s (%v)", code, hint, err)
		}
	})

	t.Run("unknown name", func(t *testing.T) {
		t.Setenv("PATH", stub("claude"))
		_, err := resolveAgent("cursor-agent")
		var ue usageError
		if !errors.As(err, &ue) {
			t.Fatalf("error = %v, want a usage error", err)
		}
	})

	t.Run("auto with one", func(t *testing.T) {
		t.Setenv("PATH", stub("claude"))
		cli, err := resolveAgent(agentAuto)
		if err != nil || cli.Name != "claude" {
			t.Fatalf("resolveAgent = %q, %v", cli.Name, err)
		}
	})

	t.Run("auto refuses to guess between two", func(t *testing.T) {
		t.Setenv("PATH", stub("claude", "codex"))
		_, err := resolveAgent(agentAuto)
		if code, msg, _ := describe(err); code != "not_found" || !strings.Contains(msg, "several") {
			t.Fatalf("code = %s, message = %s (%v)", code, msg, err)
		}
	})

	t.Run("auto with none", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := resolveAgent(agentAuto)
		if code, _, _ := describe(err); code != "not_found" {
			t.Fatalf("error = %v, want not_found", err)
		}
	})
}
