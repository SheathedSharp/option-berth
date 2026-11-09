package runsreg

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// A registration without an id still becomes an attributable run: the id is
// what lets a port keep its owner across a recycled pid, so it cannot be left
// empty just because the caller did not name one.
func TestRunFromWireAssignsAnIDWhenTheCallerSendsNone(t *testing.T) {
	rec := runFromWire(rpc.RunsRegisterParams{
		PID: 4242, Group: "  demo ", Name: " api ",
		Cmd: "npm run dev", StartedAt: "2026-01-02T03:04:05Z",
	}, "/work/demo", "cli")

	if rec.ID == "" {
		t.Fatal("registration has no id")
	}
	if rec.Group != "demo" || rec.Name != "api" {
		t.Errorf("group/name = %q/%q, want the trimmed values", rec.Group, rec.Name)
	}
	if rec.Cwd != "/work/demo" || rec.Origin != "cli" || rec.PID != 4242 {
		t.Errorf("record = %+v, want the request's own facts", rec)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !rec.StartedAt.Equal(want) {
		t.Errorf("started_at = %s, want %s", rec.StartedAt, want)
	}
}

// The caller's id wins: a re-registration of the same process must land on the
// record it already has, not on a fresh one beside it.
func TestRunFromWireKeepsTheCallersID(t *testing.T) {
	id := "run-abc"
	rec := runFromWire(rpc.RunsRegisterParams{PID: 7, ID: &id}, "/work", "cli")
	if rec.ID != id {
		t.Fatalf("id = %q, want %q", rec.ID, id)
	}
}

// An unparsable start time is not a reason to reject the registration, and an
// id-less session is not a session: recording it would stamp ports with an
// identity nothing can look up.
func TestRunFromWireDropsUntrustworthyEvidence(t *testing.T) {
	rec := runFromWire(rpc.RunsRegisterParams{
		PID: 9, StartedAt: "not a time",
		Session: &state.Session{Tool: "claude-code"},
	}, "/work", "cli")

	if !rec.StartedAt.IsZero() {
		t.Errorf("started_at = %s, want the zero time the registry fills in", rec.StartedAt)
	}
	if rec.Session.ID != "" || rec.Session.Tool != "" {
		t.Errorf("session = %+v, want none", rec.Session)
	}
}

// The home rule is the daemon's own boundary, so it is checked against the
// environment it runs in: inside is allowed, outside is refused, and the
// refusal is the caller's to lift.
//
// Both spellings of "the user's home" are set because os.UserHomeDir reads HOME
// on Unix-likes and USERPROFILE on Windows. Setting only HOME would leave a
// Windows run comparing against the real profile directory, where both the
// inside and the outside temp directories live, and this refusal would never
// happen. The production home probe is unchanged: this isolates the test's
// environment, it does not claim a native Windows run.
func TestCheckCwdEnforcesTheHomeBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	inside := filepath.Join(home, "project")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	if got, err := CheckCwd(inside, false); err != nil || got == "" {
		t.Fatalf("CheckCwd(%s) = %q, %v; want the directory itself", inside, got, err)
	}
	_, err := CheckCwd(outside, false)
	if err == nil {
		t.Fatalf("CheckCwd(%s) accepted a directory outside %s", outside, home)
	}
	var refusal *rpc.Error
	if !errors.As(err, &refusal) || refusal.Code != rpc.CodeOutsideHome {
		t.Errorf("error = %v, want an outside_home refusal", err)
	}
	if _, err := CheckCwd(outside, true); err != nil {
		t.Errorf("the caller's opt-in did not lift the refusal: %v", err)
	}
}

// checkoutWithBranch builds a directory that looks like a git checkout on
// branch, so a session that went through GitContext carries evidence the call
// actually resolved a repository.
func checkoutWithBranch(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	head := "ref: refs/heads/" + branch + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSessionOfFallsBackToTheEnvironmentWhenTheWireSessionHasNoID pins the
// precedence: an explicit session with an id is the caller's word and is kept
// exactly, while a session pointer that names no agent — or none at all — still
// lets the request's own environment supply one, with the git context of the
// working directory attached. `{"session": {}}` is a caller that sent the field
// without capturing anything, not a caller that asked for an identity-less run;
// returning it verbatim would drop the tool, worktree and branch the
// environment can still name.
func TestSessionOfFallsBackToTheEnvironmentWhenTheWireSessionHasNoID(t *testing.T) {
	cwd := checkoutWithBranch(t, "feature-wire")
	env := map[string]string{"BERTH_SESSION": "claude-code:env-1"}
	explicit := state.Session{ID: "explicit", Tool: "cursor"}

	cases := []struct {
		name    string
		params  rpc.RunsSpawnParams
		wantID  string
		wantGit bool
	}{
		{
			name:    "no session pointer falls back to env",
			params:  rpc.RunsSpawnParams{Env: env},
			wantID:  "env-1",
			wantGit: true,
		},
		{
			name:    "an id-less session pointer falls back to env",
			params:  rpc.RunsSpawnParams{Env: env, Session: &state.Session{}},
			wantID:  "env-1",
			wantGit: true,
		},
		{
			name:   "an explicit id wins and is kept verbatim",
			params: rpc.RunsSpawnParams{Env: env, Session: &explicit},
			wantID: "explicit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionOf(tc.params, cwd)
			if got.ID != tc.wantID {
				t.Fatalf("session id = %q, want %q", got.ID, tc.wantID)
			}
			if !tc.wantGit {
				if got != explicit {
					t.Fatalf("session = %+v, want the caller's session unchanged", got)
				}
				return
			}
			if got.Tool != "claude-code" {
				t.Errorf("tool = %q, want the detected agent", got.Tool)
			}
			if got.Branch != "feature-wire" {
				t.Errorf("branch = %q, want the git context of the working directory", got.Branch)
			}
		})
	}
}

// mergedEnv layers the caller's entries over the daemon's environment. With no
// overrides it returns nil, which is what tells the spawner to inherit.
func TestMergedEnvReplacesKeysInsteadOfDuplicating(t *testing.T) {
	if got := mergedEnv(nil); got != nil {
		t.Fatalf("mergedEnv(nil) = %v, want nil", got)
	}

	t.Setenv("OB_TEST_REPLACED", "daemon")
	got := mergedEnv(map[string]string{"OB_TEST_REPLACED": "caller", "OB_TEST_NEW": "value"})

	values := map[string][]string{}
	for _, pair := range got {
		for i := 0; i < len(pair); i++ {
			if pair[i] != '=' {
				continue
			}
			values[pair[:i]] = append(values[pair[:i]], pair[i+1:])
			break
		}
	}
	if v := values["OB_TEST_REPLACED"]; len(v) != 1 || v[0] != "caller" {
		t.Errorf("OB_TEST_REPLACED = %v, want exactly the caller's value", v)
	}
	if v := values["OB_TEST_NEW"]; len(v) != 1 || v[0] != "value" {
		t.Errorf("OB_TEST_NEW = %v, want the caller's value", v)
	}
}
