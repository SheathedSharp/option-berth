package state

import "testing"

// A merged table never carries the primary collector's authority, so it is
// narrow however clean the merge was: its rows are facts and its absences are
// not. A single-collector read says the same thing through Source.
func TestObservationIsNarrowForAListOrASingleSource(t *testing.T) {
	if (&Observation{State: ObservationObservable}).Narrow() {
		t.Fatal("a whole primary table was reported as narrow")
	}
	if (*Observation)(nil).Narrow() {
		t.Fatal("an absent verdict was reported as narrow")
	}
	if !(&Observation{State: ObservationObservable, Sources: []string{"ss", "netstat"}}).Narrow() {
		t.Fatal("a merged table was reported as authoritative")
	}
	if !(&Observation{State: ObservationObservable, Source: "lsof"}).Narrow() {
		t.Fatal("a single alternative collector was reported as authoritative")
	}
}

// The contributor list is compared as a set of names: an unchanged merge must
// not publish a frame just because it listed the same collectors in another
// order.
func TestSameObservationComparesContributorsAsASet(t *testing.T) {
	a := &Observation{State: ObservationObservable, Sources: []string{"ss", "netstat"}}
	b := &Observation{State: ObservationObservable, Sources: []string{"netstat", "ss"}}
	if !SameObservation(a, b) {
		t.Fatal("the same contributors in another order counted as a change")
	}
	b.Sources = []string{"ss"}
	if SameObservation(a, b) {
		t.Fatal("a merged table that lost a contributor counted as unchanged")
	}
	b.Sources = []string{"ss", "lsof"}
	if SameObservation(a, b) {
		t.Fatal("a merged table that swapped a contributor counted as unchanged")
	}
	if SameObservation(&Observation{}, &Observation{Sources: []string{"ss"}}) {
		t.Fatal("one contributor and none counted as equal")
	}
}

// The two times describe the condition, not the check, so they travel with the
// state change that produced them: a re-check at a later moment with the same
// times is not news, and the times themselves never make it news.
func TestSameObservationIgnoresTheDegradationTimes(t *testing.T) {
	a := &Observation{
		State: ObservationPartial, Code: "observation_incomplete",
		CheckedAt: "2026-01-01T00:00:00Z", DegradedSince: "2026-01-01T00:00:00Z",
	}
	b := &Observation{
		State: ObservationPartial, Code: "observation_incomplete",
		CheckedAt: "2026-01-01T00:00:05Z", DegradedSince: "2026-01-01T00:00:00Z",
	}
	if !SameObservation(a, b) {
		t.Fatal("a re-checked degradation counted as a change")
	}
	b.DegradedSince = "2026-01-01T00:00:09Z"
	if !SameObservation(a, b) {
		t.Fatal("a moved degradation start counted as a change")
	}
	healthy := &Observation{State: ObservationObservable, RecoveredAt: "2026-01-01T00:01:00Z"}
	other := &Observation{State: ObservationObservable, RecoveredAt: "2026-01-01T00:02:00Z"}
	if !SameObservation(healthy, other) {
		t.Fatal("a moved recovery time counted as a change")
	}
}
