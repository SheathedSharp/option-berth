package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// A manifest the scan sees records its checkout identity, so a later reader can
// map `${worktree}` back to the path it came from.
func TestScanRecordsTheCheckoutIdentity(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	writeConfig(t, dir, "name: demo\nservices:\n  - name: api\n    cmd: sleep 1\n")

	l := loopWith(st)
	l.ObserveConfig(dir)
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	rows, err := st.WorktreeIdentities()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("identities = %+v, want one", rows)
	}
	row := rows[0]
	wantRoot := groups.Canonical(dir)
	if row.Identity != groups.WorktreeToken(wantRoot) {
		t.Errorf("identity = %q, want the token of %q", row.Identity, wantRoot)
	}
	if row.Root != wantRoot {
		t.Errorf("root = %q, want %q", row.Root, wantRoot)
	}
	if row.Group != "demo" {
		t.Errorf("group = %q, want demo", row.Group)
	}
	if row.ConfigPath != filepath.Join(wantRoot, groups.LegacyConfigName) {
		t.Errorf("config_path = %q", row.ConfigPath)
	}
	if row.FirstSeen.IsZero() || row.LastSeen.IsZero() {
		t.Errorf("timestamps = %v / %v", row.FirstSeen, row.LastSeen)
	}
}

// Two manifests in one checkout share the checkout's `${worktree}` identity.
// The ledger keeps the checkout's own group (the shallowest manifest): that is
// the target `down --identity` can address, not a nested project that happens
// to reuse the same token.
func TestIdentityPrefersTheCheckoutsOwnManifest(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, "name: root-group\nservices: []\n")
	writeConfig(t, nested, "name: nested-group\nservices: []\n")

	l := loopWith(st)
	l.ObserveConfig(nested)
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	rows, err := st.WorktreeIdentities()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("identities = %+v, want one checkout", rows)
	}
	if rows[0].Group != "root-group" {
		t.Errorf("group = %q, want the checkout's own group", rows[0].Group)
	}
	if rows[0].ConfigPath != filepath.Join(groups.Canonical(root), groups.LegacyConfigName) {
		t.Errorf("config_path = %q, want the root manifest", rows[0].ConfigPath)
	}
}

// A root that vanished before the seed still leaves its identity behind: the
// known-roots row proves a manifest was once there, and the token is what any
// database or data directory built from it still carries.
func TestVanishedRootStillRecordsItsIdentity(t *testing.T) {
	st := openStore(t)
	gone := filepath.Join(t.TempDir(), "moved-away")
	if err := st.AddRoot(gone); err != nil {
		t.Fatal(err)
	}

	l := loopWith(st)
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	rows, err := st.WorktreeIdentities()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("identities = %+v, want the vanished root's token", rows)
	}
	wantRoot := groups.Canonical(gone)
	if rows[0].Identity != groups.WorktreeToken(wantRoot) || rows[0].Root != wantRoot {
		t.Errorf("row = %+v, want %q at %q", rows[0], groups.WorktreeToken(wantRoot), wantRoot)
	}
	if rows[0].Group != "" {
		t.Errorf("group = %q, want empty: the manifest is gone too", rows[0].Group)
	}
}
