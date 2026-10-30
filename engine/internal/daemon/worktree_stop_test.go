package daemon

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

func newStopRuntime(t *testing.T) (*Runtime, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	loop := scanner.New(scanner.Options{
		DaemonVersion: "test",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Scan:          func(scanner.Include) ([]ports.ListeningPort, error) { return nil, nil },
	})
	return &Runtime{
		Store:   st,
		Scanner: loop,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, st
}

func checkoutWithManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestExplicitStopReadsAnExistingCheckout(t *testing.T) {
	rt, _ := newStopRuntime(t)
	dir := checkoutWithManifest(t, "name: demo\nservices:\n  - name: api\n    cmd: sleep 1\n    port: auto\n")
	rt.Scanner.ObserveConfig(dir)

	got, err := killGroupTarget(rt, rpc.GroupsKillParams{WorktreePath: ptr(dir)})
	if err != nil {
		t.Fatalf("killGroupTarget: %v", err)
	}
	if got.group != "demo" || got.cfg == nil || !got.explicit {
		t.Fatalf("target = %+v, want the checkout's own manifest and group", got)
	}
	if got.claimProject == "" || got.claimWorktree == "" {
		t.Errorf("claim key = %q/%q, want the checkout's identity", got.claimProject, got.claimWorktree)
	}
}

// The point of the explicit entry: a token recorded before deletion still
// resolves to the group and claim key, with no manifest to read.
func TestExplicitStopReachesAVanishedCheckout(t *testing.T) {
	rt, st := newStopRuntime(t)
	root := filepath.Join(t.TempDir(), "gone")
	identity := groups.WorktreeToken(groups.Canonical(root))
	if err := st.RecordWorktreeIdentity(store.WorktreeIdentity{
		Identity: identity, Root: root, Group: "demo@feat",
		ConfigPath: filepath.Join(root, groups.ConfigName),
		Project:    "demo", Worktree: "feat",
	}); err != nil {
		t.Fatal(err)
	}

	for _, params := range []rpc.GroupsKillParams{
		{Identity: ptr(identity)},
		{WorktreePath: ptr(root)},
	} {
		got, err := killGroupTarget(rt, params)
		if err != nil {
			t.Fatalf("killGroupTarget(%+v): %v", params, err)
		}
		if got.group != "demo@feat" || got.cfg != nil || !got.explicit {
			t.Fatalf("target = %+v, want the recorded group without a config", got)
		}
		if got.claimProject != "demo" || got.claimWorktree != "feat" {
			t.Errorf("claim key = %q/%q", got.claimProject, got.claimWorktree)
		}
	}
}

func TestExplicitStopRejectsAnUnknownIdentity(t *testing.T) {
	rt, _ := newStopRuntime(t)
	_, err := killGroupTarget(rt, rpc.GroupsKillParams{Identity: ptr("wt_aaaaaaaaaaaa")})
	if err == nil || rpcCode(err) != rpc.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}

// Two checkouts published under one group name cannot be told apart by a
// group-scoped stop. Refuse instead of stopping the other one.
func TestExplicitStopRefusesASharedGroupName(t *testing.T) {
	rt, st := newStopRuntime(t)
	for _, root := range []string{filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")} {
		if err := st.RecordWorktreeIdentity(store.WorktreeIdentity{
			Identity: groups.WorktreeToken(groups.Canonical(root)),
			Root:     root, Group: "shared",
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.WorktreeIdentities()
	if err != nil || len(rows) != 2 {
		t.Fatalf("identities = %+v, %v", rows, err)
	}
	_, err = killGroupTarget(rt, rpc.GroupsKillParams{Identity: ptr(rows[0].Identity)})
	if err == nil || rpcCode(err) != rpc.CodeAmbiguous {
		t.Fatalf("err = %v, want ambiguous", err)
	}
}

// The user-visible question behind the gap: stopping by project name is
// ambiguous when two checkouts publish the same group name. This is the
// pinned answer — refuse, do not stop both.
func TestByNameStopIsAmbiguousWhenGroupNamesCollide(t *testing.T) {
	rt, _ := newStopRuntime(t)
	for _, body := range []string{
		"name: app\nservices: []\n",
		"name: app\nservices: []\n",
	} {
		dir := checkoutWithManifest(t, body)
		rt.Scanner.ObserveConfig(dir)
	}
	_, err := killGroupTarget(rt, rpc.GroupsKillParams{Name: "app"})
	if err == nil || rpcCode(err) != rpc.CodeAmbiguous {
		t.Fatalf("err = %v, want ambiguous", err)
	}
}

func rpcCode(err error) int {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}
