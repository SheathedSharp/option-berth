package state

import "testing"

// The verdict is part of a transition even when no row moved. A client that
// only received row changes would keep displaying a stale "blind" or "partial"
// claim after the collector recovered.
func TestDiffCarriesTheObservationVerdict(t *testing.T) {
	prev := Snapshot{Seq: 1, Observation: &Observation{
		State: ObservationPartial, Code: "observation_incomplete",
		Detail: "netstat: unreadable row", Hint: "run doctor",
	}}
	next := Snapshot{Seq: 2}
	delta := Diff(prev, next)
	if delta.Observation != nil {
		t.Fatalf("observation = %+v, want nil after recovery", delta.Observation)
	}

	next.Observation = &Observation{State: ObservationBlind, Code: "observation_blind"}
	delta = Diff(prev, next)
	if delta.Observation == nil || delta.Observation.State != ObservationBlind {
		t.Fatalf("observation = %+v, want the new blind verdict", delta.Observation)
	}
}

func TestSameObservationIgnoresCheckedAt(t *testing.T) {
	a := &Observation{State: ObservationPartial, Code: "observation_incomplete", CheckedAt: "2026-01-01T00:00:00Z"}
	b := &Observation{State: ObservationPartial, Code: "observation_incomplete", CheckedAt: "2026-01-01T00:00:05Z"}
	if !SameObservation(a, b) {
		t.Fatal("a re-checked identical verdict counted as a change")
	}
	b.Detail = "different evidence"
	if SameObservation(a, b) {
		t.Fatal("different evidence counted as the same verdict")
	}
	if SameObservation(nil, &Observation{State: ObservationObservable}) {
		t.Fatal("absent and observable verdicts compared equal")
	}
}
