package runsreg

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// TestCheckCwdReportsAnUnavailableWorkingDirectory covers the one cwd the
// daemon cannot name: the caller sent none and the daemon's own working
// directory is gone. Both spellings of "no usable cwd" are their own error, and
// the message is asserted because the code alone cannot tell them apart — a
// failed `os.Getwd` that fell through to the Abs branch would answer
// invalid_params either way, with a different remedy for the user.
//
// The deletion is of this process's own t.TempDir, restored by the cleanup
// registered before it; on a filesystem that refuses to remove the active
// directory the case is skipped rather than asserted.
func TestCheckCwdReportsAnUnavailableWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restoring cwd: %v", err)
		}
	})
	if err := os.Remove(dir); err != nil {
		t.Skipf("filesystem cannot remove the active cwd: %v", err)
	}

	_, err = CheckCwd("", true)
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != rpc.CodeInvalidParams ||
		failure.Message != "cwd is required" {
		t.Fatalf("missing cwd = %v, want original required-cwd RPC error", err)
	}

	// A relative cwd in a directory that no longer exists has nothing to be
	// relative to, so it is refused as an unusable path rather than resolved
	// against a stale one.
	_, err = CheckCwd("relative-child", true)
	failure = nil
	if !errors.As(err, &failure) || failure.Code != rpc.CodeInvalidParams ||
		!strings.HasPrefix(failure.Message, "cwd is not a usable path:") {
		t.Fatalf("relative path in unavailable cwd = %v", err)
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

	values := envValues(t, got)
	if v := values["OB_TEST_REPLACED"]; len(v) != 1 || v[0] != "caller" {
		t.Errorf("OB_TEST_REPLACED = %v, want exactly the caller's value", v)
	}
	if v := values["OB_TEST_NEW"]; len(v) != 1 || v[0] != "value" {
		t.Errorf("OB_TEST_NEW = %v, want the caller's value", v)
	}
}

// Every key in the merged list arrives once, with a value, so an override
// replaces the inherited entry instead of sitting beside it. The total says the
// same thing from the other side: nothing is appended twice and nothing is
// added that nobody asked for. Overrides the inherited environment already
// carries do not lengthen the list, they replace.
func TestMergedEnvEmitsOneWellFormedEntryPerKey(t *testing.T) {
	const inherited = "OB_TEST_INHERITED"
	t.Setenv(inherited, "daemon")

	over := map[string]string{"OB_TEST_OVER": "caller", "OB_TEST_EXTRA": "second"}
	got := mergedEnv(over)

	values := envValues(t, got)
	inheritedKeys := envValues(t, os.Environ())
	if v := values[inherited]; len(v) != 1 || v[0] != "daemon" {
		t.Errorf("%s = %v, want exactly the daemon's value", inherited, v)
	}
	for key, want := range over {
		if v := values[key]; len(v) != 1 || v[0] != want {
			t.Errorf("%s = %v, want exactly %q", key, v, want)
		}
	}

	added := 0
	for key := range over {
		if _, seen := inheritedKeys[key]; !seen {
			added++
		}
	}
	if want := len(os.Environ()) + added; len(got) != want {
		t.Errorf("mergedEnv returned %d entries, want %d: the inherited list plus the overrides it did not already carry", len(got), want)
	}
}

// The inherited environment is not an upper bound on the caller's overrides: a
// request that sends more variables than the daemon inherited still merges
// every one of them, and does so without deciding the result's size up front.
func TestMergedEnvMergesMoreOverridesThanTheInheritedEnvironment(t *testing.T) {
	inherited := len(os.Environ())
	over := make(map[string]string, inherited+10)
	for i := 0; i < inherited+10; i++ {
		over["OB_TEST_BULK_"+strconv.Itoa(i)] = "value-" + strconv.Itoa(i)
	}

	values := envValues(t, mergedEnv(over))
	for key, want := range over {
		if v := values[key]; len(v) != 1 || v[0] != want {
			t.Fatalf("%s = %v, want exactly %q", key, v, want)
		}
	}
}

// A Windows drive designator puts the entry's first `=` in the key:
// `=C:=C:\work` is the key `=C:` with the value `C:\work`, and it has to
// survive as it came. This is the shape our envValues has to read the way Go's
// own exec does; the test does not claim that a Windows run was made here.
func TestEnvValuesKeepsWindowsDriveEntries(t *testing.T) {
	values := envValues(t, []string{`=C:=C:\work`, "OB_SAMPLE=ok"})
	if v := values["=C:"]; len(v) != 1 || v[0] != `C:\work` {
		t.Errorf(`=C: = %v, want exactly the drive entry's value`, v)
	}
	if v := values["OB_SAMPLE"]; len(v) != 1 || v[0] != "ok" {
		t.Errorf("OB_SAMPLE = %v, want the ordinary entry's value", v)
	}
}

// envValues indexes a merged environment by key. It accepts what exec accepts,
// including a Windows drive designator, and refuses only an entry that names no
// variable at all — which is exactly the empty item a bad append would insert.
func envValues(t *testing.T, pairs []string) map[string][]string {
	t.Helper()
	values := make(map[string][]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := splitEnvEntry(pair)
		if !ok {
			t.Fatalf("merged environment entry %q names no variable", pair)
		}
		values[key] = append(values[key], value)
	}
	return values
}

// splitEnvEntry cuts one entry into its key and value. A leading '=' belongs to
// the key when the entry names a Windows drive, so `=C:=C:\work` is the key
// `=C:` with the value `C:\work`; every other entry splits at its first '='.
// It reports false only for an entry that names nothing.
func splitEnvEntry(pair string) (key, value string, ok bool) {
	sep := strings.IndexByte(pair, '=')
	if sep < 0 {
		return "", "", false
	}
	if sep == 0 {
		// The designator's own '=' is the next one, and there has to be a
		// drive letter between the two.
		designator := strings.IndexByte(pair[1:], '=')
		if designator < 1 {
			return "", "", false
		}
		sep = 1 + designator
	}
	return pair[:sep], pair[sep+1:], true
}
