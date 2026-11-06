package store

import (
	"fmt"
	"testing"
	"time"
)

// historyAt returns a filter with no port and no time bound.
func historyAt(limit int) HistoryFilter { return HistoryFilter{Limit: limit} }

// record writes events through the one write path the ring has.
func record(t *testing.T, s *Store, events ...HistoryEvent) {
	t.Helper()
	if err := s.AppendBatch(events); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
}

func TestTheRingReadsBackNewestFirst(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	record(t, s,
		HistoryEvent{At: base, Kind: EventPortUp, Port: 3000, PID: 100, DisplayName: "api", Group: "shop"},
		HistoryEvent{At: base.Add(time.Minute), Kind: EventPortUp, Port: 5173, PID: 101, DisplayName: "web", Group: "shop"},
		HistoryEvent{At: base.Add(2 * time.Minute), Kind: EventPortRestarted, Port: 3000, PID: 102, DisplayName: "api", Group: "shop"},
		HistoryEvent{At: base.Add(3 * time.Minute), Kind: EventPortDown, Port: 5173, PID: 101, DisplayName: "web", Group: "shop"},
	)

	got, err := s.History(historyAt(0))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("History returned %d events, want 4", len(got))
	}
	if got[0].Kind != EventPortDown || got[3].Kind != EventPortUp || got[3].Port != 3000 {
		t.Errorf("History is not newest-first: %+v", got)
	}
	if !got[0].At.Equal(base.Add(3 * time.Minute)) {
		t.Errorf("At round-tripped as %v, want %v", got[0].At, base.Add(3*time.Minute))
	}
	if got[0].DisplayName != "web" || got[0].Group != "shop" || got[0].PID != 101 {
		t.Errorf("row fields round-tripped wrong: %+v", got[0])
	}
}

// A port filter narrows the read to that port and nothing else.
func TestTheReadCanBeNarrowedToOnePort(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	record(t, s,
		HistoryEvent{At: base, Kind: EventPortUp, Port: 3000},
		HistoryEvent{At: base.Add(time.Minute), Kind: EventPortUp, Port: 5173},
		HistoryEvent{At: base.Add(2 * time.Minute), Kind: EventPortRestarted, Port: 3000},
	)

	port := 3000
	only, err := s.History(HistoryFilter{Port: &port})
	if err != nil {
		t.Fatalf("History(port): %v", err)
	}
	if len(only) != 2 {
		t.Fatalf("History(3000) returned %d events, want 2", len(only))
	}
	for _, event := range only {
		if event.Port != 3000 {
			t.Errorf("the port filter leaked port %d", event.Port)
		}
	}
}

// A `since` bound drops everything older than it.
func TestTheReadCanBeNarrowedByTime(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	record(t, s,
		HistoryEvent{At: base, Kind: EventPortUp, Port: 3000},
		HistoryEvent{At: base.Add(time.Minute), Kind: EventPortUp, Port: 5173},
		HistoryEvent{At: base.Add(2 * time.Minute), Kind: EventPortDown, Port: 5173},
	)

	// A bound is inclusive, so a `since` of two minutes keeps the event written
	// at two minutes and the one after it, and drops the two before.
	recent, err := s.History(HistoryFilter{Since: base.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("History(since): %v", err)
	}
	if len(recent) != 1 || !recent[0].At.Equal(base.Add(2*time.Minute)) {
		t.Errorf("History(since=+2m) = %+v, want only the event at two minutes", recent)
	}

	// A bound before the oldest event keeps everything.
	all, err := s.History(HistoryFilter{Since: base.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("History(since): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("History(since before the oldest) returned %d events, want 3", len(all))
	}
}

// A read that names no limit takes the default, and one that names a limit gets
// exactly that many of the newest.
func TestTheReadHonoursItsRowBudget(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	batch := make([]HistoryEvent, 0, DefaultHistoryLimit+10)
	for i := range cap(batch) {
		batch = append(batch, HistoryEvent{
			At: base.Add(time.Duration(i) * time.Second), Kind: EventPortUp, Port: 3000 + i,
		})
	}
	record(t, s, batch...)

	all, err := s.History(historyAt(0))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(all) != DefaultHistoryLimit {
		t.Errorf("an unbudgeted read returned %d events, want %d", len(all), DefaultHistoryLimit)
	}

	one, err := s.History(historyAt(1))
	if err != nil {
		t.Fatalf("History(limit=1): %v", err)
	}
	if len(one) != 1 || one[0].Port != 3000+cap(batch)-1 {
		t.Errorf("History(limit=1) = %+v, want the newest event only", one)
	}
}

// An event with no time is stamped when it is written, and one with no kind is
// refused: no reader could interpret that row.
func TestAnEventNeedsAKindButNotATime(t *testing.T) {
	s := openTemp(t)
	before := time.Now().Add(-time.Second)
	record(t, s, HistoryEvent{Kind: EventPortUp, Port: 3000})

	got, err := s.History(historyAt(1))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].At.Before(before) {
		t.Errorf("a zero At was not stamped with now: %v", got[0].At)
	}
	if err := s.AppendBatch([]HistoryEvent{{Port: 3000}}); err == nil {
		t.Error("an event with no kind was accepted")
	}
}

// A batch is all-or-nothing: one bad event refuses the whole write.
func TestABatchIsAllOrNothing(t *testing.T) {
	s := openTemp(t)
	err := s.AppendBatch([]HistoryEvent{
		{Kind: EventPortUp, Port: 3000},
		{Port: 3001}, // no kind: the whole batch must roll back
	})
	if err == nil {
		t.Fatal("AppendBatch accepted an event with no kind")
	}
	if count, err := s.HistoryCount(); err != nil || count != 0 {
		t.Errorf("a refused batch left %d rows (%v), want 0", count, err)
	}
	if err := s.AppendBatch(nil); err != nil {
		t.Errorf("AppendBatch(nil) = %v, want nil", err)
	}
}

func TestTheRingSurvivesAReopen(t *testing.T) {
	path := t.TempDir() + "/option-berth.db"
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	record(t, first, HistoryEvent{Kind: EventPortUp, Port: 3000, DisplayName: "api"})
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	got, err := second.History(historyAt(0))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 1 || got[0].DisplayName != "api" {
		t.Errorf("history after reopen = %+v, want the api event", got)
	}
}

// The ring trigger keeps exactly the newest HistoryCapacity rows: the oldest
// ones are evicted, and the count stops growing.
func TestTheRingKeepsItsCapacity(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 10 500 rows")
	}
	s := openTemp(t)
	const overflow = 500
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	batch := make([]HistoryEvent, 0, HistoryCapacity+overflow)
	for i := range HistoryCapacity + overflow {
		batch = append(batch, HistoryEvent{
			At:          base.Add(time.Duration(i) * time.Second),
			Kind:        EventPortUp,
			Port:        3000,
			PID:         i,
			DisplayName: fmt.Sprintf("event-%d", i),
		})
	}
	record(t, s, batch...)

	if count, err := s.HistoryCount(); err != nil || count != HistoryCapacity {
		t.Fatalf("the ring holds %d rows (%v), want exactly %d", count, err, HistoryCapacity)
	}

	newest, err := s.History(historyAt(1))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if want := fmt.Sprintf("event-%d", HistoryCapacity+overflow-1); newest[0].DisplayName != want {
		t.Errorf("newest row = %q, want %q", newest[0].DisplayName, want)
	}

	// The overflowing rows were dropped from the old end; the one right after
	// them is the oldest survivor.
	oldest, err := s.History(historyAt(HistoryCapacity))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if want := fmt.Sprintf("event-%d", overflow); oldest[len(oldest)-1].DisplayName != want {
		t.Errorf("oldest surviving row = %q, want %q", oldest[len(oldest)-1].DisplayName, want)
	}

	// One more event evicts one more row and the count stays put.
	record(t, s, HistoryEvent{Kind: EventPortDown, Port: 3000, DisplayName: "last"})
	if count, err := s.HistoryCount(); err != nil || count != HistoryCapacity {
		t.Errorf("after one more event the ring holds %d rows (%v), want %d", count, err, HistoryCapacity)
	}
}
