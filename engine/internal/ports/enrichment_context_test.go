package ports

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// No test using the package cache is parallel. Preserve both caches, including
// timestamps and original immutable maps, rather than leaving state for later tests.
func isolateSignalCaches(t *testing.T) {
	t.Helper()
	displaySignalCache.Lock()
	seq := displaySignalCache.seq
	displaySignalCache.seq = 0
	at, covered, info, cwds := displaySignalCache.at, displaySignalCache.covered, displaySignalCache.pidInfo, displaySignalCache.cwds
	displaySignalCache.at = time.Time{}
	displaySignalCache.covered, displaySignalCache.pidInfo, displaySignalCache.cwds = nil, nil, nil
	displaySignalCache.Unlock()
	parents := scanParentTable.Load()
	scanParentTable.Store(nil)
	t.Cleanup(func() {
		displaySignalCache.Lock()
		displaySignalCache.seq = seq
		displaySignalCache.at, displaySignalCache.covered = at, covered
		displaySignalCache.pidInfo, displaySignalCache.cwds = info, cwds
		displaySignalCache.Unlock()
		scanParentTable.Store(parents)
	})
}

func TestEnrichmentPreCancelledPreservesRows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, stats := range []bool{false, true} {
		rows := []ListeningPort{{PID: 42, Port: 8080, Command: "known", Connections: 7, MemoryRSS: 13, Type: PortTypeDocker, DockerContainer: "owned"}}
		before := append([]ListeningPort(nil), rows...)
		var err error
		if stats {
			err = EnrichStatsContext(ctx, rows, map[string]*DockerStatsEntry{"owned": {MemoryRSS: 99}})
		} else {
			err = EnrichContext(ctx, rows)
		}
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(rows, before) {
			t.Fatalf("stats=%v err=%v rows=%+v", stats, err, rows)
		}
	}
}

func TestMetadataCancellationCannotWarmCaches(t *testing.T) {
	for _, stage := range []string{"table", "cwd"} {
		t.Run(stage, func(t *testing.T) {
			isolateSignalCaches(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cwdCalls := 0
			info, cwds, err := readDisplayNameSignals(ctx, []int{42}, func(context.Context) map[int]pidEntry {
				if stage == "table" {
					cancel()
				}
				return map[int]pidEntry{42: {ppid: 1, cmd: "late"}}
			}, func(context.Context, []int) map[int]string {
				cwdCalls++
				cancel()
				return map[int]string{42: "/late"}
			})
			if !errors.Is(err, context.Canceled) || info != nil || cwds != nil {
				t.Fatalf("cancelled candidate escaped: %v %v %v", info, cwds, err)
			}
			if stage == "table" && cwdCalls != 0 {
				t.Fatal("cwd IO admitted after cancellation")
			}
			if !displaySignalCache.at.IsZero() || scanParentTable.Load() != nil {
				t.Fatal("cancelled metadata warmed an identity cache")
			}
		})
	}
}

func TestMetadataPreCancelledRejectsCacheHit(t *testing.T) {
	isolateSignalCaches(t)
	displaySignalCache.at = time.Now()
	displaySignalCache.covered = map[int]struct{}{42: {}}
	displaySignalCache.pidInfo = map[int]pidEntry{42: {cmd: "cached"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	info, cwds, err := readDisplayNameSignals(ctx, []int{42}, func(context.Context) map[int]pidEntry { t.Fatal("table called"); return nil }, func(context.Context, []int) map[int]string { t.Fatal("cwd called"); return nil })
	if !errors.Is(err, context.Canceled) || info != nil || cwds != nil {
		t.Fatalf("cancelled cache hit: %v %v %v", info, cwds, err)
	}
}

func TestMetadataCancellationKeepsPriorObservation(t *testing.T) {
	isolateSignalCaches(t)
	oldAt := time.Now()
	displaySignalCache.at = oldAt
	displaySignalCache.covered = map[int]struct{}{7: {}}
	displaySignalCache.pidInfo = map[int]pidEntry{7: {cmd: "prior"}}
	displaySignalCache.cwds = map[int]string{7: "/prior"}
	rememberParentEntries(map[int]pidEntry{7: {ppid: 1, cmd: "prior"}})
	oldParent := scanParentTable.Load()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, err := readDisplayNameSignals(ctx, []int{42}, func(context.Context) map[int]pidEntry { return map[int]pidEntry{42: {cmd: "late"}} }, func(context.Context, []int) map[int]string { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || displaySignalCache.at != oldAt || displaySignalCache.pidInfo[7].cmd != "prior" || scanParentTable.Load() != oldParent {
		t.Fatal("cancelled refresh modified prior observation")
	}
}

func TestMetadataPartialEvidenceAndRecovery(t *testing.T) {
	isolateSignalCaches(t)
	tableCalls := 0
	table := func(context.Context) map[int]pidEntry {
		tableCalls++
		if tableCalls == 1 {
			return map[int]pidEntry{7: {ppid: 1, cmd: "seen"}}
		}
		return map[int]pidEntry{7: {ppid: 1, cmd: "seen"}, 42: {ppid: 7, cmd: "recovered"}}
	}
	dirs := func(context.Context, []int) map[int]string { return map[int]string{7: "/visible"} }
	info, cwds, err := readDisplayNameSignals(context.Background(), []int{7, 42}, table, dirs)
	if err != nil || info[7].cmd != "seen" || cwds[7] != "/visible" {
		t.Fatalf("lost partial facts: %v %v %v", info, cwds, err)
	}
	// Windows intentionally caches cwd coverage without a Unix ps table.
	if runtime.GOOS == "windows" {
		return
	}
	if !displaySignalCache.at.IsZero() {
		t.Fatal("partial table claimed coverage of missing PID")
	}
	info, _, err = readDisplayNameSignals(context.Background(), []int{7, 42}, table, dirs)
	if err != nil || info[42].cmd != "recovered" || tableCalls != 2 {
		t.Fatal("missing identity hidden behind TTL")
	}
	_, _, err = readDisplayNameSignals(context.Background(), []int{7, 42}, table, dirs)
	if err != nil || tableCalls != 2 {
		t.Fatal("valid metadata cache hit restarted IO")
	}
}

func TestMetadataAdaptersPreCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := batchGetProcessTableContext(ctx); len(got) != 0 {
		t.Fatal(got)
	}
	if got := batchGetCommandsWindowsContext(ctx, []string{"42"}); len(got) != 0 {
		t.Fatal(got)
	}
	if got := batchGetCwdsContext(ctx, []int{42}); len(got) != 0 {
		t.Fatal(got)
	}
	if got := countThreadsDarwinContext(ctx, []int{42}); len(got) != 0 {
		t.Fatal(got)
	}
	if got := connectionCountsContext(ctx); len(got) != 0 {
		t.Fatal(got)
	}
	rows := []ListeningPort{{PID: 42, Connections: 8}}
	fillProcessNamesWindowsContext(ctx, rows, nil)
	applyConnectionCountsContext(ctx, rows)
	if rows[0].Process != "" || rows[0].Connections != 8 {
		t.Fatal(rows)
	}
}

func TestConnectionCollectionFailurePreservesObservation(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	rows := []ListeningPort{{PID: 42, Port: 8080, Connections: 7}}
	applyConnectionCounts(rows)
	if rows[0].Connections != 7 {
		t.Fatalf("collector failure invented zero connections: %+v", rows)
	}
}

func TestMetadataCacheHitDoesNotAllocateOrCollect(t *testing.T) {
	isolateSignalCaches(t)
	pids := []int{42}
	tableCalls, cwdCalls := 0, 0
	table := func(context.Context) map[int]pidEntry {
		tableCalls++
		return map[int]pidEntry{42: {ppid: 1, cmd: "cached"}}
	}
	dirs := func(context.Context, []int) map[int]string { cwdCalls++; return map[int]string{42: "/cached"} }
	ctx := context.Background()
	if _, _, err := readDisplayNameSignals(ctx, pids, table, dirs); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		info, cwds, err := readDisplayNameSignals(ctx, pids, table, dirs)
		if err != nil || info[42].cmd != "cached" || cwds[42] != "/cached" {
			t.Fatal("cache hit changed facts")
		}
	})
	if allocs != 0 || tableCalls != 1 || cwdCalls != 1 {
		t.Fatalf("hit costs: allocs=%v table=%d cwd=%d", allocs, tableCalls, cwdCalls)
	}
}
