package attn

import "testing"

func TestSelectTaskTargets(t *testing.T) {
	a := Artifact{EventID: "event", StateRevision: "revision", Events: []Event{{Kind: EventServiceFailed, Service: "worker", RunID: "old"}, {Kind: EventServiceFailed, Service: "worker", RunID: "new"}, {Kind: EventServiceFailed, Service: "foreign", RunID: "other"}}, Evidence: []Evidence{{Service: "worker", RunID: "old"}, {Service: "worker", RunID: "new"}, {Service: "foreign", RunID: "other"}}}
	got, ok := Select(a, nil, []string{"old"})
	if !ok || len(got.Events) != 1 || got.Event.RunID != "old" || len(got.Evidence) != 1 || got.EventID != a.EventID || got.StateRevision != a.StateRevision {
		t.Fatalf("wrong exact run selection: %+v", got)
	}
	got, ok = Select(a, []string{"worker"}, nil)
	if !ok || len(got.Events) != 2 || len(got.Evidence) != 2 {
		t.Fatalf("wrong service selection: %+v", got)
	}
	if _, ok = Select(a, nil, []string{"missing"}); ok {
		t.Fatal("unrelated event selected")
	}
	if len(a.Events) != 3 || len(a.Evidence) != 3 {
		t.Fatal("mutated source")
	}
}

func TestPortlessTaskRunFailureRequiresExactAssociation(t *testing.T) {
	f := Facts{WorktreeRoot: "/synthetic/worktree", Runs: []Service{{Name: "worker", RunID: "worker-1", RunOnly: true, LastExit: &Exit{RunID: "worker-1", Reason: "crashed", Code: 7}}}}
	events := ClassifyAll(f)
	if len(events) != 1 || !events[0].RunOnly || events[0].RunID != "worker-1" {
		t.Fatalf("missing portless exit: %+v", events)
	}
	a := Artifact{Events: events}
	if _, ok := Select(a, nil, nil); ok {
		t.Fatal("unassociated ad-hoc failure selected")
	}
	if _, ok := Select(a, []string{"worker"}, nil); ok {
		t.Fatal("service name adopted ad-hoc run")
	}
	if _, ok := Select(a, nil, []string{"worker-2"}); ok {
		t.Fatal("replacement run adopted")
	}
	if _, ok := Select(a, nil, []string{"worker-1"}); !ok {
		t.Fatal("explicit task run excluded")
	}
	f.Runs[0].LastExit.Reason = "stopped"
	if len(ClassifyAll(f)) != 0 {
		t.Fatal("normal stop became anomaly")
	}
}
