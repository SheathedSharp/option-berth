package rpc

import (
	"encoding/json"
	"testing"
)

// A pointer cannot tell `"worktree_ports": null` from the key being absent, and
// the two mean opposite things: absent leaves the key in the file alone, null
// removes it. UnmarshalJSON is what keeps them apart, so the params type is
// checked here on the bytes a client actually sends.
func TestWorktreePortsDistinguishesNullFromAbsent(t *testing.T) {
	cases := []struct {
		name    string
		wire    string
		wantSet bool
		wantVal *int
	}{
		{name: "absent", wire: `{"path": "/p/oberth.yaml"}`, wantSet: false},
		{name: "null", wire: `{"path": "/p/oberth.yaml", "worktree_ports": null}`, wantSet: true},
		{name: "number", wire: `{"path": "/p/oberth.yaml", "worktree_ports": 4}`, wantSet: true, wantVal: intPtr(4)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p GroupsConfigSetParams
			if err := json.Unmarshal([]byte(tc.wire), &p); err != nil {
				t.Fatalf("decoding %s: %v", tc.wire, err)
			}
			if p.WorktreePortsSent != tc.wantSet {
				t.Fatalf("sent = %v, want %v", p.WorktreePortsSent, tc.wantSet)
			}
			change := p.WorktreePortsChange()
			if change.Set != tc.wantSet {
				t.Fatalf("change.Set = %v, want %v", change.Set, tc.wantSet)
			}
			if !tc.wantSet {
				return
			}
			switch {
			case tc.wantVal == nil && change.Value != nil:
				t.Errorf("change.Value = %d, want a cleared key", *change.Value)
			case tc.wantVal != nil && (change.Value == nil || *change.Value != *tc.wantVal):
				t.Errorf("change.Value = %v, want %d", change.Value, *tc.wantVal)
			}
		})
	}
}

// A patch that names no worktree_ports still decodes the rest of the call, so
// the key-set read must not turn a params typo into a silent empty edit.
func TestGroupsConfigSetParamsDecodesTheRestOfTheCall(t *testing.T) {
	wire := `{"path": "/p/oberth.yaml", "remove": ["api"], "add": [{"name": "web", "port": 8080}]}`
	var p GroupsConfigSetParams
	if err := json.Unmarshal([]byte(wire), &p); err != nil {
		t.Fatalf("decoding %s: %v", wire, err)
	}
	if p.Path != "/p/oberth.yaml" {
		t.Errorf("path = %q", p.Path)
	}
	if len(p.Remove) != 1 || p.Remove[0] != "api" {
		t.Errorf("remove = %v, want [api]", p.Remove)
	}
	if len(p.Add) != 1 || p.Add[0].Name != "web" || p.Add[0].Port != 8080 {
		t.Errorf("add = %+v, want one web service on 8080", p.Add)
	}
	if p.WorktreePortsSent {
		t.Error("worktree_ports was not sent, but the params say it was")
	}
}

func intPtr(n int) *int { return &n }
