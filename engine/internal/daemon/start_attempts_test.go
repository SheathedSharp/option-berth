package daemon

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestJoinStartAttemptsCopiesWithoutTouchingTheSnapshot(t *testing.T) {
	rt := &Runtime{}
	prepareMS := int64(800)
	RegisterStartAttemptLookup(func(got *Runtime, group, service string) (StartAttempt, bool) {
		if got != rt || group != "demo" || service != "web" {
			return StartAttempt{}, false
		}
		return StartAttempt{
			Phase:         "prepare",
			StartedAt:     time.Now().Add(-1500 * time.Millisecond),
			ReadyTimeout:  30 * time.Second,
			WaitingOn:     []string{"db"},
			LastPrepareMS: &prepareMS,
		}, true
	})
	t.Cleanup(func() { RegisterStartAttemptLookup(nil) })

	cached := []state.Group{{
		Name: "demo",
		Services: []state.Service{
			{Name: "web"},
			{Name: "api"},
		},
	}}
	joined := joinStartAttempts(rt, cached)

	if cached[0].Services[0].Starting != nil {
		t.Fatal("joining mutated the cached snapshot")
	}
	starting := joined[0].Services[0].Starting
	if starting == nil {
		t.Fatal("the matching service got no starting object")
	}
	if starting.Phase != "prepare" || starting.ReadyTimeout != "30s" {
		t.Errorf("starting = %+v, want the attempt's phase and budget", starting)
	}
	if starting.ElapsedMS < 1500 {
		t.Errorf("elapsed_ms = %d, want the time since the attempt began", starting.ElapsedMS)
	}
	if len(starting.WaitingOn) != 1 || starting.WaitingOn[0] != "db" {
		t.Errorf("waiting_on = %v, want db", starting.WaitingOn)
	}
	if starting.LastPrepareMS == nil || *starting.LastPrepareMS != 800 {
		t.Errorf("last_prepare_ms = %v, want 800", starting.LastPrepareMS)
	}
	if joined[0].Services[1].Starting != nil {
		t.Fatal("a service without an attempt got one")
	}
}

func TestJoinStartAttemptsWithoutALookupIsANoOp(t *testing.T) {
	rt := &Runtime{}
	cached := []state.Group{{Name: "demo", Services: []state.Service{{Name: "web"}}}}
	joined := joinStartAttempts(rt, cached)
	if joined[0].Services[0].Starting != nil {
		t.Fatal("an unregistered lookup produced a starting object")
	}
}
