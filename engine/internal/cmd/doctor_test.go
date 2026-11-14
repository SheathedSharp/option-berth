package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/doctor"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/spf13/cobra"
)

// doctorSample is one report with a row of every status: ok, skip, fail and
// warn, each carrying the fields the table renders for it.
func doctorSample() rpc.RuntimeServiceDoctorResult {
	return rpc.RuntimeServiceDoctorResult{
		OK:                    false,
		Version:               "v2.3.4",
		RuntimeServiceVersion: "v2.3.4",
		Checks: []rpc.DoctorCheck{
			{ID: "cli_on_path", Status: doctor.StatusOK, Summary: "oberth resolves from PATH", Detail: "/usr/local/bin/oberth"},
			{ID: "docker", Status: doctor.StatusSkip, Summary: "docker is not installed"},
			{
				ID: "config_parses", Status: doctor.StatusFail,
				Summary: "config.yaml does not parse",
				Detail:  "/home/dev/.option-berth/config.yaml:1:7: did not find expected ','\n  list: [broken\n        ^",
				Fix:     "oberth doctor --fix", Fixable: true,
			},
			{
				ID: "hooks_installed", Status: doctor.StatusWarn,
				Summary: "the option-berth hooks are not installed",
				Fix:     "oberth install hooks --claude-code", Fixable: true,
			},
		},
	}
}

func TestRenderDoctorTable(t *testing.T) {
	previous := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = previous })

	var out bytes.Buffer
	renderDoctor(&out, doctorSample())
	rendered := out.String()

	for _, expected := range []string{
		"ok    cli_on_path",
		"skip  docker",
		"FAIL  config_parses",
		"warn  hooks_installed",
		// The fix hint rides on the rows that need acting on...
		"→ oberth install hooks --claude-code",
		// ...and the evidence is spelled out under a failure.
		"list: [broken",
		"^",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("table is missing %q:\n%s", expected, rendered)
		}
	}
	// A healthy row must not carry a fix hint or its detail.
	if strings.Contains(rendered, "/usr/local/bin/oberth") {
		t.Errorf("an ok row should not print its detail:\n%s", rendered)
	}
	if !strings.Contains(rendered, "something is wrong: 1 fail, 1 warn, 1 ok, 1 skip") {
		t.Errorf("verdict is missing or wrong:\n%s", rendered)
	}
}

func TestRenderDoctorVerdicts(t *testing.T) {
	previous := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = previous })

	cases := []struct {
		label   string
		input   rpc.RuntimeServiceDoctorResult
		verdict string
	}{
		{
			label: "all ok",
			input: rpc.RuntimeServiceDoctorResult{OK: true, Checks: []rpc.DoctorCheck{
				{ID: "only", Status: doctor.StatusOK, Summary: "nothing to report"},
			}},
			verdict: "all good: 1 ok",
		},
		{
			label: "warnings only",
			input: rpc.RuntimeServiceDoctorResult{OK: true, Checks: []rpc.DoctorCheck{
				{ID: "only", Status: doctor.StatusWarn, Summary: "check the port table"},
			}},
			verdict: "mostly healthy: 1 warn",
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			var out bytes.Buffer
			renderDoctor(&out, tc.input)
			if !strings.Contains(out.String(), tc.verdict) {
				t.Errorf("verdict = %q, want it to contain %q", out.String(), tc.verdict)
			}
		})
	}
}

// TestDoctorJSON drives the real command, so the document asserted here is the
// document a script parses.
func TestDoctorJSON(t *testing.T) {
	withDoctorFlags(t, func() {
		doctorJSONFlag = true
		doctorOnlyFlag = []string{"docker", "db_ok"}
	})

	out := captureStdout(t, func() {
		if err := doctorRun(testCommand(), nil); err != nil && !errors.Is(err, errSilent) {
			t.Fatalf("oberth doctor --json: %v", err)
		}
	})

	var doc rpc.RuntimeServiceDoctorResult
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(doc.Checks) != 2 {
		t.Fatalf("got %d checks, want the 2 that were selected: %+v", len(doc.Checks), doc.Checks)
	}
	ids := []string{doc.Checks[0].ID, doc.Checks[1].ID}
	if ids[0] != "db_ok" || ids[1] != "docker" {
		t.Errorf("ids = %v, want [db_ok docker] in table order", ids)
	}
	for _, check := range doc.Checks {
		switch check.Status {
		case doctor.StatusOK, doctor.StatusWarn, doctor.StatusFail, doctor.StatusSkip:
		default:
			t.Errorf("%s has status %q", check.ID, check.Status)
		}
	}
	if doc.Version == "" {
		t.Error("the report carries no version")
	}
}

// TestDoctorExitsNonZeroOnAFailure pins the contract a script depends on: the
// command answers errSilent, which Execute turns into exit 1 and no further
// output.
func TestDoctorExitsNonZeroOnAFailure(t *testing.T) {
	withDoctorFlags(t, func() {
		// No runtime service runs in an isolated test binary, so this one fails.
		doctorOnlyFlag = []string{"runtime_service_reachable"}
	})
	runErr := errors.New("")
	captureStdout(t, func() { runErr = doctorRun(testCommand(), nil) })
	if !errors.Is(runErr, errSilent) {
		t.Fatalf("error = %v, want errSilent", runErr)
	}
}

func TestDoctorRejectsAnUnknownCheck(t *testing.T) {
	withDoctorFlags(t, func() { doctorOnlyFlag = []string{"not_a_check"} })
	runErr := doctorRun(testCommand(), nil)
	if runErr == nil || !strings.Contains(runErr.Error(), "not_a_check") {
		t.Fatalf("error = %v, want it to name the unknown check", runErr)
	}
}

func TestDoctorProjectMustBeADirectory(t *testing.T) {
	target := os.DevNull
	withDoctorFlags(t, func() { doctorProjectFlag = target })
	if _, err := doctorProject(); err == nil {
		t.Fatal("a --project that is not a directory should be refused")
	}
}

// TestEveryFixableCheckHasAFix keeps the two lists in step: a check that
// advertises Fixable with nothing registered in doctorFixes would print an
// offer `--fix` cannot keep.
func TestEveryFixableCheckHasAFix(t *testing.T) {
	registered := map[string]bool{}
	for _, fix := range doctorFixes() {
		registered[fix.id] = true
	}
	// The ids the doctor package marks fixable, spelled out here so a new one
	// has to be considered rather than silently unhandled.
	for _, id := range []string{
		"config_parses", "project_config", "runtime_service_reachable",
	} {
		if !registered[id] {
			t.Errorf("no fix registered for %s", id)
		}
	}
	for _, fix := range doctorFixes() {
		if !contains(doctor.IDs(), fix.id) {
			t.Errorf("fix %s repairs a check that does not exist", fix.id)
		}
	}
}

// TestDoctorFixMatchingIsExact: the comment above the fix table used to claim a
// dotted id matched by prefix, while the code compared strings. The code was
// right — a dotted check names one check — so this pins exact matching.
func TestDoctorFixMatchingIsExact(t *testing.T) {
	fix := doctorFix{id: "config_parses"}
	for _, id := range []string{"config_parses", "config_parses.claude_code", "config", "config_parse"} {
		expected := id == "config_parses"
		if matched := fix.matches(id); matched != expected {
			t.Errorf("matches(%q) = %v, want %v", id, matched, expected)
		}
	}
}

// TestSelectDoctorFixesFollowsTheReport: only failed or warned fixable rows
// are repaired, in report order, and a fixable row without a registered fix
// contributes nothing rather than a guessed repair.
func TestSelectDoctorFixesFollowsTheReport(t *testing.T) {
	todo := selectDoctorFixes(doctorSample())
	if len(todo) != 1 || todo[0].id != "config_parses" {
		t.Fatalf("todo = %+v, want only config_parses", todo)
	}
}

func contains(list []string, want string) bool {
	found := false
	for _, item := range list {
		if item == want {
			found = true
			break
		}
	}
	return found
}

// ------------------------------------------------------------------ shared ---

// withDoctorFlags installs one set of package-level flag values for a test and
// restores the previous ones when it ends.
func withDoctorFlags(t *testing.T, set func()) {
	t.Helper()
	type flagSet struct {
		json    bool
		fix     bool
		yes     bool
		only    []string
		project string
	}
	saved := flagSet{doctorJSONFlag, doctorFixFlag, doctorYesFlag, doctorOnlyFlag, doctorProjectFlag}
	t.Cleanup(func() {
		doctorJSONFlag, doctorFixFlag, doctorYesFlag = saved.json, saved.fix, saved.yes
		doctorOnlyFlag, doctorProjectFlag = saved.only, saved.project
	})
	doctorJSONFlag, doctorFixFlag, doctorYesFlag = false, false, false
	doctorOnlyFlag, doctorProjectFlag = nil, ""
	set()
}

func testCommand() *cobra.Command {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	return c
}

// applyDoctorFixesReport drives the repair round around one report and returns
// what it said on stderr plus how many repairs it applied.
func applyDoctorFixesReport(t *testing.T, report rpc.RuntimeServiceDoctorResult) (string, int, error) {
	t.Helper()
	var applied int
	var err error
	out := captureStderr(t, func() {
		applied, err = applyDoctorFixes(context.Background(), testCommand(), report)
	})
	return out, applied, err
}

// TestApplyDoctorFixesRepairsNothingWhenEveryCheckIsFine: ok and skip rows are
// not repairs, however fixable the check says it is.
func TestApplyDoctorFixesRepairsNothingWhenEveryCheckIsFine(t *testing.T) {
	out, applied, err := applyDoctorFixesReport(t, rpc.RuntimeServiceDoctorResult{
		OK: true,
		Checks: []rpc.DoctorCheck{
			{ID: "config_parses", Status: doctor.StatusOK, Fixable: true},
			{ID: "project_config", Status: doctor.StatusSkip, Fixable: true},
		},
	})
	if err != nil || applied != 0 {
		t.Fatalf("applied = %d, err = %v; want nothing to repair", applied, err)
	}
	if !strings.Contains(out, "nothing to fix automatically") {
		t.Fatalf("stderr = %q, want the nothing-to-fix sentence", out)
	}
}

// TestApplyDoctorFixesAsksBeforeTouchingAnything: the plan lists the repairs
// that actually exist — a fixable check with no registered repair is not
// offered — and a run that is not confirmed changes nothing on disk.
func TestApplyDoctorFixesAsksBeforeTouchingAnything(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	withDoctorFlags(t, func() { doctorYesFlag = false })
	answerNextPrompt(t)

	out, applied, err := applyDoctorFixesReport(t, doctorSample())
	if err != nil {
		t.Fatalf("applyDoctorFixes: %v", err)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want none without confirmation", applied)
	}
	if !strings.Contains(out, "repair config_parses") {
		t.Errorf("stderr = %q, want the registered repair in the plan", out)
	}
	if strings.Contains(out, "hooks_installed") {
		t.Errorf("stderr = %q, want no plan entry for a check without a repair", out)
	}
	if !strings.Contains(out, "nothing was changed") {
		t.Errorf("stderr = %q, want the refusal sentence", out)
	}
	if _, statErr := os.Stat(config.Path()); !os.IsNotExist(statErr) {
		t.Errorf("config %s exists after a refused --fix (stat: %v)", config.Path(), statErr)
	}
}

// TestApplyDoctorFixesRepairsTheBrokenConfig runs the real registered repair:
// the unparseable file is kept under its broken name and a fresh commented
// template takes its place.
func TestApplyDoctorFixesRepairsTheBrokenConfig(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	withDoctorFlags(t, func() { doctorYesFlag = true })

	path := config.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("creating the config directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("runtime_service: [broken\n"), 0o600); err != nil {
		t.Fatalf("writing the broken config: %v", err)
	}

	out, applied, err := applyDoctorFixesReport(t, doctorSample())
	if err != nil {
		t.Fatalf("applyDoctorFixes: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want the one registered repair; stderr:\n%s", applied, out)
	}
	if !strings.Contains(out, "config_parses: moved the broken config to") {
		t.Fatalf("stderr = %q, want the repair's own account", out)
	}

	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		t.Fatalf("reading the config directory: %v", readErr)
	}
	kept := 0
	for _, entry := range entries {
		if strings.Contains(entry.Name(), doctor.BrokenSuffix) {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("broken copies kept = %d, want the unparseable file moved aside once", kept)
	}
	fresh, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("no fresh config was written: %v", readErr)
	}
	if strings.Contains(string(fresh), "[broken") {
		t.Fatal("the broken contents are still the config")
	}
	if !strings.Contains(string(fresh), "runtime_service:") {
		t.Fatalf("fresh config = %q, want the template", fresh)
	}
}

// TestApplyDoctorFixesReportsAFailedRepair: a repair that cannot run is
// reported and counted as not applied, rather than being claimed as done.
func TestApplyDoctorFixesReportsAFailedRepair(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	withDoctorFlags(t, func() { doctorYesFlag = true })

	out, applied, err := applyDoctorFixesReport(t, rpc.RuntimeServiceDoctorResult{
		Checks: []rpc.DoctorCheck{{ID: "config_parses", Status: doctor.StatusFail, Fixable: true}},
	})
	if err != nil {
		t.Fatalf("applyDoctorFixes: %v", err)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want none for a repair that could not run", applied)
	}
	if !strings.Contains(out, "could not repair config_parses") {
		t.Fatalf("stderr = %q, want the failure named", out)
	}
}
