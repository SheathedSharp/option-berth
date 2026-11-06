package ports

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

// The callback barriers order actual cache admissions, not wall-clock sleeps.
func TestMetadataOlderCompletionCannotReplaceNewer(t *testing.T) {
	isolateSignalCaches(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		info, dirs, err := readDisplayNameSignals(context.Background(), []int{42},
			func(context.Context) map[int]processFacts { return map[int]processFacts{42: {ppid: 1, command: "old"}} },
			func(context.Context, []int) map[int]string {
				close(entered)
				<-release
				return map[int]string{42: "/old"}
			})
		if err == nil && (info[42].command != "old" || dirs[42] != "/old") {
			err = errors.New("caller lost its own completed observation")
		}
		done <- err
	}()
	<-entered
	// No Fatal until the owned reader is released and joined.
	info, dirs, err := readDisplayNameSignals(context.Background(), []int{42},
		func(context.Context) map[int]processFacts { return map[int]processFacts{42: {ppid: 2, command: "new"}} },
		func(context.Context, []int) map[int]string { return map[int]string{42: "/new"} })
	close(release)
	oldErr := <-done
	if err != nil || oldErr != nil {
		t.Fatalf("new=%v old=%v", err, oldErr)
	}
	if info[42].command != "new" || dirs[42] != "/new" {
		t.Fatal("newer returned maps were mutated")
	}
	neverTable := func(context.Context) map[int]processFacts { t.Error("fresh newer cache missed"); return nil }
	neverDirs := func(context.Context, []int) map[int]string { t.Error("fresh newer cwd cache missed"); return nil }
	cached, cwd, err := readDisplayNameSignals(context.Background(), []int{42}, neverTable, neverDirs)
	if err != nil || cached[42].command != "new" || cwd[42] != "/new" {
		t.Fatalf("older completion replaced newer cache: %v %v %v", cached, cwd, err)
	}
	p := scanParentTable.Load()
	if p == nil || p.entries[42].ppid != 2 {
		t.Fatalf("older completion replaced newer parent facts: %+v", p)
	}
}

func TestMetadataFutureCacheTimestampForcesObservation(t *testing.T) {
	isolateSignalCaches(t)
	displaySignalCache.at = time.Now().Add(time.Hour)
	displaySignalCache.covered = map[int]struct{}{42: {}}
	displaySignalCache.pidInfo = map[int]processFacts{42: {command: "future"}}
	calls := 0
	info, _, err := readDisplayNameSignals(context.Background(), []int{42},
		func(context.Context) map[int]processFacts {
			calls++
			return map[int]processFacts{42: {command: "observed"}}
		},
		func(context.Context, []int) map[int]string { return nil })
	if err != nil || calls != 1 || info[42].command != "observed" {
		t.Fatalf("future timestamp treated as fresh: calls=%d info=%v error=%v", calls, info, err)
	}
}

func TestMetadataPartialObservationRevokesPreviousCoverage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cwd cache has separate CIM identity")
	}
	isolateSignalCaches(t)
	calls := 0
	table := func(context.Context) map[int]processFacts {
		calls++
		cmd := "new"
		if calls == 1 {
			cmd = "old"
		}
		return map[int]processFacts{7: {ppid: 1, command: cmd}}
	}
	dirs := func(context.Context, []int) map[int]string { return nil }
	if _, _, err := readDisplayNameSignals(context.Background(), []int{7}, table, dirs); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readDisplayNameSignals(context.Background(), []int{7, 42}, table, dirs); err != nil {
		t.Fatal(err)
	}
	info, _, err := readDisplayNameSignals(context.Background(), []int{7}, table, dirs)
	if err != nil || calls != 3 || info[7].command != "new" {
		t.Fatalf("partial newer observation left old coverage fresh: calls=%d info=%v error=%v", calls, info, err)
	}
}

func TestMetadataCancelledNewerReadDoesNotDisplaceOwner(t *testing.T) {
	isolateSignalCaches(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := readDisplayNameSignals(context.Background(), []int{42},
			func(context.Context) map[int]processFacts {
				return map[int]processFacts{42: {ppid: 1, command: "owner"}}
			},
			func(context.Context, []int) map[int]string { close(entered); <-release; return nil })
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err := readDisplayNameSignals(ctx, []int{42},
		func(context.Context) map[int]processFacts {
			cancel()
			return map[int]processFacts{42: {command: "cancelled"}}
		},
		func(context.Context, []int) map[int]string { return nil })
	close(release)
	ownerErr := <-done
	if !errors.Is(err, context.Canceled) || ownerErr != nil {
		t.Fatalf("cancel=%v owner=%v", err, ownerErr)
	}
	if displaySignalCache.pidInfo[42].command != "owner" {
		t.Fatal("cancelled request suppressed completed owner")
	}
}

func TestParentObservationOrderDoesNotUseCompletionClock(t *testing.T) {
	isolateSignalCaches(t)
	earlier, later := beginProcessObservation(), beginProcessObservation()
	// Timestamp equality is intentional: sequence is the ordering evidence.
	earlier.at = later.at
	rememberObservedProcessFacts(map[int]processFacts{42: {ppid: 2}}, later)
	rememberObservedProcessFacts(map[int]processFacts{42: {ppid: 1}}, earlier)
	snap := scanParentTable.Load()
	if snap == nil || snap.entries[42].ppid != 2 || snap.seq != later.seq {
		t.Fatalf("older Unix observation replaced newer CIM facts: %+v", snap)
	}
	rememberObservedProcessFacts(map[int]processFacts{42: {ppid: 3}}, earlier)
	if scanParentTable.Load() != snap {
		t.Fatal("older CIM completion changed parent snapshot")
	}
}

func TestParentObservationAgeStartsBeforeCollection(t *testing.T) {
	isolateSignalCaches(t)
	observed := beginProcessObservation()
	observed.at = time.Now().Add(-displaySignalCacheTTL - time.Second)
	rememberObservedProcessFacts(map[int]processFacts{42: {ppid: 1, startedAt: "Thu Jan  1 00:00:00 2026"}}, observed)
	snap := scanParentTable.Load()
	if snap == nil || !snap.at.Equal(observed.at) || recentScanEntries() != nil {
		t.Fatalf("slow collection renewed stale identity: %+v", snap)
	}
}

func TestParentObservationConcurrentWritersKeepLatestAdmission(t *testing.T) {
	isolateSignalCaches(t)
	const n = 32
	tickets := make([]processObservation, n)
	for i := range tickets {
		tickets[i] = beginProcessObservation()
	}
	start, done := make(chan struct{}), make(chan struct{}, n)
	for i := range tickets {
		go func(i int) {
			<-start
			rememberObservedProcessFacts(map[int]processFacts{42: {ppid: i + 1}}, tickets[i])
			done <- struct{}{}
		}(i)
	}
	close(start)
	for range n {
		<-done
	}
	snap := scanParentTable.Load()
	if snap == nil || snap.seq != tickets[n-1].seq || snap.entries[42].ppid != n {
		t.Fatalf("concurrent parent publication regressed: %+v", snap)
	}
}
