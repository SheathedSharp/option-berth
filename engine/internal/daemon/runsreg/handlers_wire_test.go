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
func TestCheckCwdEnforcesTheHomeBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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
