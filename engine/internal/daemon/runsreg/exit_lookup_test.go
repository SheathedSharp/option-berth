package runsreg

import (
	"testing"
	"time"
)

func TestExitForRunIgnoresReusedPIDAndFindsItsOwnHistory(t *testing.T) {
	r := testRegistry()
	start := time.Unix(1000, 123)
	own := Exit{Record: Record{ID: "original", PID: 42, StartedAt: start},
		Code: 7, ExitedAt: start.Add(time.Second)}
	r.exits = []Exit{
		own,
		{Record: Record{ID: "replacement", PID: 42, StartedAt: start.Add(time.Second)},
			Code: 9, ExitedAt: start.Add(2 * time.Second)},
	}
	got, ok := r.ExitForRun("original", 42, start)
	if !ok || got.ID != own.ID || got.Code != 7 {
		t.Fatalf("own generation hidden by later PID use: %+v / %v", got, ok)
	}
	if _, ok := r.ExitForRun("missing", 42, start); ok {
		t.Fatal("numeric PID substituted for the requested run ID")
	}
}

func TestExitForRunRequiresExactBirthAndValidChronology(t *testing.T) {
	r := testRegistry()
	start := time.Unix(1000, 123)
	r.exits = []Exit{{Record: Record{ID: "run", PID: 42, StartedAt: start}, ExitedAt: start}}
	if _, ok := r.ExitForRun("run", 42, start.In(time.FixedZone("offset", 3600))); !ok {
		t.Fatal("equal instants with different zones did not match")
	}
	for _, query := range []struct {
		id   string
		pid  int
		born time.Time
	}{
		{"", 42, start}, {"run", 0, start}, {"run", 43, start},
		{"run", 42, time.Time{}}, {"run", 42, start.Add(time.Nanosecond)},
	} {
		if _, ok := r.ExitForRun(query.id, query.pid, query.born); ok {
			t.Fatal("incomplete or different generation matched")
		}
	}
	r.exits[0].ExitedAt = start.Add(-time.Nanosecond)
	if _, ok := r.ExitForRun("run", 42, start); ok {
		t.Fatal("exit before the run began was accepted")
	}
}
