package runtimesvc

import (
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// TestGroupsListServesTheSnapshotsGroups holds `groups.list` to the groups the
// scan already resolved: the list an agent reads and the groups a subscriber
// sees come from one snapshot, so the two cannot drift apart.
func TestGroupsListServesTheSnapshotsGroups(t *testing.T) {
	cl := newPortsHarness(t, t.Context())

	var listed rpc.GroupsListResult
	if e := cl.call("groups.list", rpc.Empty{}, &listed); e != nil {
		t.Fatalf("groups.list: %v", e)
	}
	// An empty answer is still an array: a client that ranges over the field
	// must never have to special-case "no groups".
	if listed.Groups == nil {
		t.Fatal("groups.list answered null, want an array")
	}

	var snap struct {
		Groups []state.Group `json:"groups"`
	}
	if e := cl.call("state.snapshot", rpc.StateSnapshotParams{}, &snap); e != nil {
		t.Fatalf("state.snapshot: %v", e)
	}
	if len(snap.Groups) != len(listed.Groups) {
		t.Fatalf("the snapshot holds %d groups, groups.list answered %d",
			len(snap.Groups), len(listed.Groups))
	}
	for i, g := range snap.Groups {
		if listed.Groups[i].Name != g.Name {
			t.Errorf("group %d: groups.list says %q, the snapshot says %q",
				i, listed.Groups[i].Name, g.Name)
		}
	}
}
