package state

import "testing"

// A checkout switching branches changes nothing but its group's branch, and
// subscribers still need to see it: the group page shows the branch.
func TestACheckoutChangeReachesSubscribers(t *testing.T) {
	at := func(branch, worktree string) Group {
		return Group{
			Name: "shop@feat", Repo: "shop", Worktree: worktree, Branch: branch,
			Members: []int{}, Services: []Service{},
		}
	}
	before := Snapshot{Groups: []Group{at("main", "feat")}}

	for _, after := range []Group{at("fix", "feat"), at("main", "")} {
		delta := diff(before, Snapshot{Groups: []Group{after}}, false)
		if len(delta.Groups.Updated) != 1 {
			t.Errorf("changing %+v published %+v, want one update", after, delta.Groups)
		}
	}
	if delta := diff(before, before, false); len(delta.Groups.Updated) != 0 {
		t.Errorf("an unchanged group published %+v", delta.Groups)
	}
}
