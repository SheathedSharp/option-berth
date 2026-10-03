package ports

import (
	"reflect"
	"testing"
)

func TestFullSampleRejectsContradictoryBirth(t *testing.T) {
	for _, birth := range []string{"2026-01-02T00:00:00Z", "unparseable"} {
		t.Run(birth, func(t *testing.T) {
			p := ListeningPort{PID: 42, StartedAt: "2026-01-01T00:00:00Z", MemoryRSS: 17, ThreadCount: 2, State: "known"}
			before := p
			ProcSample{StartedAt: birth, MemoryRSS: 999, ThreadCount: 8, State: "running"}.Apply(&p)
			if !reflect.DeepEqual(p, before) {
				t.Fatalf("full sample crossed process identity: got=%+v before=%+v", p, before)
			}
		})
	}
}

func TestFullSampleKeepsEquivalentAndUnknownBirthRules(t *testing.T) {
	for _, tc := range []struct{ name, row, sample string }{
		{"equal", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"},
		{"same instant", "2026-01-01T00:00:00Z", "2026-01-01T09:00:00+09:00"},
		{"unknown row", "", "2026-01-01T00:00:00Z"},
		{"unknown sample", "2026-01-01T00:00:00Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ListeningPort{PID: 42, StartedAt: tc.row, MemoryRSS: 17, ThreadCount: 2}
			ProcSample{StartedAt: tc.sample, MemoryRSS: 999}.Apply(&p)
			wantBirth := tc.row
			if wantBirth == "" {
				wantBirth = tc.sample
			}
			if p.MemoryRSS != 999 || p.ThreadCount != 2 || p.StartedAt != wantBirth {
				t.Fatalf("existing sample semantics changed: %+v", p)
			}
		})
	}
}
