package ports

import (
	"reflect"
	"testing"
	"time"
)

func TestProcessFactsExpireEvenWhenRefreshFails(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snap := &parentSnapshot{at: now, entries: map[int]procEntry{42: {ppid: 7, startedAt: "2026-01-01T00:00:00Z"}}}
	for _, tc := range []struct {
		name  string
		age   time.Duration
		fresh bool
	}{
		{"fresh", 0, true}, {"just before expiry", displaySignalCacheTTL - time.Nanosecond, true},
		{"exact expiry", displaySignalCacheTTL, false}, {"expired", time.Hour, false}, {"clock moved backwards", -time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refreshes := 0
			recent := func() *parentSnapshot { return freshParentEntries(snap, now.Add(tc.age)) }
			failed := func() map[int]pidEntry { refreshes++; return nil }
			_, ok := processStartWith(42, recent, failed)
			if ok != tc.fresh {
				t.Fatalf("identity known=%v, want %v", ok, tc.fresh)
			}
			table := collectParentTable(func() map[int]int { return nil }, recent, failed)
			if (table[42] == 7) != tc.fresh {
				t.Fatalf("ancestry = %v, want fresh=%v", table, tc.fresh)
			}
			if tc.fresh && refreshes != 0 || !tc.fresh && refreshes != 2 {
				t.Fatalf("refresh calls = %d", refreshes)
			}
		})
	}
}

func TestProcessStartRecoversWithFreshPartialEvidence(t *testing.T) {
	now := time.Now()
	var snap *parentSnapshot
	refreshes := 0
	refresh := func() map[int]pidEntry {
		refreshes++
		snap = &parentSnapshot{at: now, entries: map[int]procEntry{42: {ppid: 7, startedAt: "2026-01-01T00:00:00Z"}}}
		return nil
	}
	recent := func() *parentSnapshot { return freshParentEntries(snap, now) }
	if _, ok := processStartWith(42, recent, refresh); !ok || refreshes != 1 {
		t.Fatal("fresh evidence not accepted")
	}
	if _, ok := processStartWith(99, recent, refresh); ok {
		t.Fatal("partial table invented missing PID")
	}
	if _, ok := processStartWith(0, recent, refresh); ok || refreshes != 1 {
		t.Fatal("invalid PID reached collector")
	}
}

func TestPartialSignalsPreserveListenerAndUnknowns(t *testing.T) {
	rows := []ListeningPort{
		{PID: 42, Port: 8080, Process: "worker"},
		{PID: 43, Port: 8081, Command: "known command", PPID: 9, StartedAt: "known time"},
		{PID: 0, Port: 8082},
	}
	applyDisplayNameSignals(rows, map[int]pidEntry{43: {ppid: 0, cmd: "", startedAt: "bad time"}}, map[int]string{43: "/project"})
	want := []ListeningPort{
		{PID: 42, Port: 8080, Process: "worker"},
		{PID: 43, Port: 8081, Command: "known command", PPID: 9, StartedAt: "known time", Cwd: "/project"},
		{PID: 0, Port: 8082},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("partial evidence changed unrelated facts: %+v", rows)
	}
	if cwdGone(rows[0].Cwd) {
		t.Fatal("unreadable cwd treated as a removed directory")
	}
	applyDisplayNameSignals(rows, map[int]pidEntry{42: {ppid: 7, cmd: "worker --run", startedAt: "Thu Jan  1 00:00:00 2026"}, 7: {cmd: "launcher"}}, map[int]string{42: "/recovered"})
	if rows[0].Cwd != "/recovered" || rows[0].PPID != 7 || rows[0].ParentCmd != "launcher" || rows[0].StartedAt == "" {
		t.Fatalf("recovered facts = %+v", rows[0])
	}
}

func TestProcessTableRejectsInvalidIdentityRows(t *testing.T) {
	input := "-1 2 Thu Jan  1 00:00:00 2026 bad\n0 2 Thu Jan  1 00:00:00 2026 bad\n42 -2 Thu Jan  1 00:00:00 2026 bad\n43 1 Thu Jan  1 00:00:00 2026 good --flag\n"
	got := parseProcessTable(input)
	if len(got) != 1 || got[43].cmd != "good --flag" {
		t.Fatalf("process table = %+v", got)
	}
}

func TestExpiredProcessStartDoesNotReviveAfterCollectorFailure(t *testing.T) {
	previous := scanParentTable.Load()
	t.Cleanup(func() { scanParentTable.Store(previous) })
	// An empty PATH makes the Unix refresh fail without invoking a host command;
	// Windows has no Unix process-table adapter at all.
	t.Setenv("PATH", t.TempDir())
	scanParentTable.Store(&parentSnapshot{at: time.Now().Add(-time.Hour), entries: map[int]procEntry{42: {ppid: 7, startedAt: "2026-01-01T00:00:00Z"}}})
	if started, ok := ProcessStart(42); ok {
		t.Fatalf("expired identity revived after failed collector: %v", started)
	}
}
