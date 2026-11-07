package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// A caller that has already left must not be admitted, and must not keep the
// run gate: the next live scan still runs.
func TestAlreadyCancelledScanIsNotAdmitted(t *testing.T) {
	var scans int
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) {
		scans++
		return []ports.ListeningPort{{Port: 9, PID: 1, Process: "app"}}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.scanAndPublishContext(ctx, Include{})
	if scans != 0 {
		t.Fatalf("cancelled scan collected %d times", scans)
	}
	l.scanAndPublishContext(context.Background(), Include{})
	if scans != 1 || len(l.Cached().Ports) != 1 || l.Cached().Ports[0].Port != 9 {
		t.Fatalf("scans=%d cached=%+v, the refused admission stuck the gate or dropped the row", scans, l.Cached().Ports)
	}
}

func TestReadReusesAScanThatBeganAfterTheAsk(t *testing.T) {
	frozen := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	l := New(Options{
		Now:  func() time.Time { return frozen },
		Scan: fixedRows(ports.ListeningPort{Port: 9, PID: 7, Process: "app", MemoryRSS: 100}),
	})
	if _, _, _, err := l.scanLocked(Include{}, Include{}, false); err != nil {
		t.Fatal(err)
	}
	// Generation 0 is before this scan. A read that asked then may take it.
	snap, reused, err := l.reuseIfFresh(0, Include{Stats: true}, mayCoalesce)
	if err != nil || !reused || len(snap.Ports) != 1 || snap.Ports[0].Port != 9 || snap.Ports[0].Stats == nil {
		t.Fatalf("reused=%v err=%v snap=%+v, want the scan that began after the ask, stats included", reused, err, snap.Ports)
	}
	cases := []struct {
		name    string
		asked   uint64
		include Include
		mode    scanMode
	}{
		{"the scan already running", l.scanGeneration(), Include{Stats: true}, mayCoalesce},
		{"health never coalesces", 0, Include{Health: true}, mayCoalesce},
		{"a forced scan never coalesces", 0, Include{}, forceScan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, reused, err := l.reuseIfFresh(tc.asked, tc.include, tc.mode)
			if err != nil || reused {
				t.Fatalf("reused=%v err=%v, want a real scan", reused, err)
			}
		})
	}
}

func TestSampleStartThatCannotBeParsedStaysUnknown(t *testing.T) {
	pid := 4242
	recorded := "2026-11-07T00:00:00Z"
	groups := []state.Group{{Services: []state.Service{{
		Name: "worker", Running: true, PID: &pid, StartedAt: &recorded,
	}}}}
	FillServiceStatsContext(context.Background(), groups, func([]int) map[int]ports.ProcSample {
		return map[int]ports.ProcSample{pid: {MemoryRSS: 10, StartedAt: "not-a-timestamp"}}
	})
	if groups[0].Services[0].Stats == nil || groups[0].Services[0].Stats.MemoryRSS != 10 {
		t.Fatalf("stats = %+v, an unreadable sample start is unknown, not a different process", groups[0].Services[0].Stats)
	}
}

func TestEventTimeUsesTheStampOrTheScanTime(t *testing.T) {
	fallback := time.Date(2026, 11, 7, 1, 2, 3, 0, time.UTC)
	if got := eventTime("2026-11-07T04:05:06Z", fallback); !got.Equal(time.Date(2026, 11, 7, 4, 5, 6, 0, time.UTC)) {
		t.Fatalf("parsed = %s", got)
	}
	if got := eventTime("not-a-time", fallback); !got.Equal(fallback) {
		t.Fatalf("fallback = %s", got)
	}
}
