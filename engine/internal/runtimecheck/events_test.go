package runtimecheck

import (
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"testing"
)

func TestScopedChangesIgnoreServiceResourceSampling(t *testing.T) {
	root := "/synthetic/a"
	before := state.Snapshot{Seq: 1, Groups: []state.Group{{Name: "a", RootDir: &root, Services: []state.Service{{Name: "worker", Running: true}}}}}
	after := before
	after.Seq = 2
	after.Groups = append([]state.Group{}, before.Groups...)
	after.Groups[0].Services = append([]state.Service{}, before.Groups[0].Services...)
	after.Groups[0].Services[0].Stats = &state.Stats{}
	after.Groups[0].Resources = &state.Resources{}
	if got := state.StateChangesForTransition(before, after, nil); len(got) != 0 {
		t.Fatalf("sampling woke task: %+v", got)
	}
	after.Groups[0].Services[0].Running = false
	if got := state.StateChangesForTransition(before, after, nil); len(got) == 0 {
		t.Fatal("lost runtime transition")
	}
}
