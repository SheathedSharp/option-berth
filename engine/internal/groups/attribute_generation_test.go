package groups

import (
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestAttributeWithRevokesPreviousRunEvidence(t *testing.T) {
	pp := []ports.ListeningPort{{Port: 4000, PID: 12, Process: "listener"}}
	before, index := AttributeWith(pp, runSet{port: 4000, id: "old", group: "project", name: "web"}, nil)
	if before[0].Run == nil {
		t.Fatal("fixture did not stamp run ownership")
	}
	after, _ := AttributeWith(pp, NoRuns{}, index)
	if after[0].Run != nil || after[0].Group != nil || after[0].DisplayName == "web" {
		t.Fatalf("revoked run survived re-attribution: %+v", after[0])
	}
	if pp[0].RunID != "" || pp[0].RunGroup != "" || pp[0].Tag != "" || pp[0].RunRootPID != 0 {
		t.Fatalf("raw batch retained revoked ownership: %+v", pp[0])
	}
	if before[0].Run.ID != "old" || before[0].DisplayName != "web" {
		t.Fatal("re-attribution mutated the already published snapshot")
	}
}

type rotatingRegistry struct{ calls int }

func (r *rotatingRegistry) Run(state.Port) (state.Run, bool) {
	r.calls++
	if r.calls == 1 {
		return state.Run{ID: "old", Group: "before", Name: "web", RootPID: 12}, true
	}
	return state.Run{ID: "replacement", Group: "after", Name: "other", RootPID: 12}, true
}

func TestAttributeWithUsesOneOwnershipLookupPerListener(t *testing.T) {
	reg := &rotatingRegistry{}
	pp := []ports.ListeningPort{{Port: 4000, PID: 12, Process: "listener"}}
	resolved, _ := AttributeWith(pp, reg, nil)
	p := resolved[0]
	if p.Run == nil || p.Group == nil || p.Run.Group != *p.Group || p.Run.Name != p.DisplayName {
		t.Fatalf("mixed ownership generations in one row: %+v (run %+v)", p, p.Run)
	}
	if reg.calls != 1 {
		t.Fatalf("ownership lookups = %d, want 1", reg.calls)
	}
}

// Run ownership was absent when captured; a later session lookup must not
// introduce a new generation independently of that ownership decision.
type sessionOnlyRegistry struct{ NoRuns }

func (sessionOnlyRegistry) Session(state.Port) (state.Session, bool) {
	return state.Session{ID: "new-session"}, true
}

func TestAttributeWithDoesNotStampSessionWithoutCapturedRun(t *testing.T) {
	pp := []ports.ListeningPort{{Port: 4000, PID: 12, Process: "listener"}}
	resolved, _ := AttributeWith(pp, sessionOnlyRegistry{}, nil)
	if resolved[0].Run != nil || resolved[0].Session != nil {
		t.Fatalf("session appeared without captured run ownership: %+v", resolved[0])
	}
}
