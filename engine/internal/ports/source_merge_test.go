package ports

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// ssRow and the two headers are the shapes the Linux collectors print, so a
// merge test can say which rung answered for one port.
const (
	ssRow       = "LISTEN 0      128    127.0.0.1:18201    0.0.0.0:*    users:((\"node\",pid=4242,fd=20))\n"
	ssHeader    = "State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"
	linuxHeader = "Proto Recv-Q Send-Q Local Address           Foreign Address         State       PID/Program name\n"
)

// netstatRow is one `netstat -tlnp` row: the pid column is where Linux netstat
// puts the owner, or `-` when this user may not read it.
func netstatRow(port, pid string) string {
	owner := "-"
	if pid != "" {
		owner = pid + "/node"
	}
	return "tcp        0      0 127.0.0.1:" + port + "         0.0.0.0:*               LISTEN     " + owner + "\n"
}

// mergedScan runs the merge over a fake chain answer and returns what a caller
// would publish.
func mergedScan(t *testing.T, platform string, limit int, answers map[string]collectorAnswer) ([]ListeningPort, []string, error) {
	t.Helper()
	run := &fakeCollectors{answers: answers}
	return collectListenersSources(platform, run.run, limit)
}

// Two collectors that both read the machine contribute two tables and every
// row: the point of a merge is the union, not one table replacing the other.
func TestMergeUnionsTwoCollectorTables(t *testing.T) {
	rows, sources, err := mergedScan(t, "linux", 2, map[string]collectorAnswer{
		"ss":      {out: ssHeader + ssRow},
		"netstat": {out: linuxHeader + netstatRow("18202", "77")},
	})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if want := []string{"ss", "netstat"}; !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %v, want %v in chain order", sources, want)
	}
	if len(rows) != 2 || rows[0].Port != 18201 || rows[1].Port != 18202 {
		t.Fatalf("rows = %+v, want both collectors' listeners", rows)
	}
}

// A limit larger than the platform's chain is clamped to the chain: nothing
// tries to invent a third collector on a two-rung platform.
func TestMergeLimitClampsToTheChainLength(t *testing.T) {
	run := &fakeCollectors{answers: map[string]collectorAnswer{
		"ss":      {out: ssHeader + ssRow},
		"netstat": {out: linuxHeader + netstatRow("18202", "77")},
	}}
	rows, sources, err := collectListenersSources("linux", run.run, 9)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v, want both rungs' rows", rows, err)
	}
	if want := []string{"ss", "netstat"}; !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %v, want %v", sources, want)
	}
	if want := []string{"ss", "netstat"}; !reflect.DeepEqual(run.tried, want) {
		t.Fatalf("collectors tried = %v, want the whole chain once", run.tried)
	}
	// Windows has a single rung, so any limit above it reads exactly that one.
	single := &fakeCollectors{answers: map[string]collectorAnswer{"netstat": {out: netstatRow("18202", "77")}}}
	if _, sources, err := collectListenersSources("windows", single.run, 4); err != nil ||
		!reflect.DeepEqual(sources, []string{"netstat"}) {
		t.Fatalf("sources=%v err=%v, want the single Windows rung", sources, err)
	}
}

// The merge learns what the first table could not say: a socket both collectors
// saw keeps the row that names its owner.
func TestMergePrefersTheRowThatNamesItsOwner(t *testing.T) {
	rows, sources, err := mergedScan(t, "linux", 2, map[string]collectorAnswer{
		"ss":      {out: ssHeader + "LISTEN 0      128    127.0.0.1:18201    0.0.0.0:*\n"},
		"netstat": {out: linuxHeader + netstatRow("18201", "4242")},
	})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if len(rows) != 1 || rows[0].PID != 4242 {
		t.Fatalf("rows = %+v, want the owner the second collector reported", rows)
	}
	if want := []string{"ss", "netstat"}; !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %v, want %v", sources, want)
	}
}

// Two rows that both name an owner are one socket, and the earlier rung keeps
// it: a merge never argues with the table that comes first in the chain.
func TestMergeKeepsTheFirstOwnerOnADuplicate(t *testing.T) {
	rows, _, err := mergedScan(t, "linux", 2, map[string]collectorAnswer{
		"ss":      {out: ssHeader + ssRow},
		"netstat": {out: linuxHeader + netstatRow("18201", "9999")},
	})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if len(rows) != 1 || rows[0].PID != 4242 {
		t.Fatalf("rows = %+v, want the first rung's owner", rows)
	}
}

// A collector that looked and saw nothing is a contributor, not a silent skip:
// its name is published because it covered this table and found no listener.
func TestMergeNamesACollectorThatSawNothing(t *testing.T) {
	rows, sources, err := mergedScan(t, "linux", 2, map[string]collectorAnswer{
		"ss":      {out: ssHeader},
		"netstat": {out: linuxHeader + netstatRow("18202", "77")},
	})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if len(rows) != 1 || rows[0].Port != 18202 {
		t.Fatalf("rows = %+v, want only the collector that saw a listener", rows)
	}
	if want := []string{"ss", "netstat"}; !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %v, want the empty table named too", want)
	}
}

// A failed collector contributes nothing: its rows do not exist and its name
// would claim it covered a table it never read.
func TestMergeDropsAFailedCollector(t *testing.T) {
	rows, sources, err := mergedScan(t, "linux", 2, map[string]collectorAnswer{
		"ss":      {err: exec.ErrNotFound},
		"netstat": {out: linuxHeader + netstatRow("18202", "77")},
	})
	if err != nil {
		t.Fatalf("merge failed although one rung answered: %v", err)
	}
	if len(rows) != 1 || rows[0].Port != 18202 {
		t.Fatalf("rows = %+v, want the answering collector's row", rows)
	}
	if want := []string{"netstat"}; !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %v, want only the collector that read a table", sources)
	}
}

// With no table anywhere the round is the primary's failure, and every rung's
// reason rides in the same sentence: "ss is not installed and netstat was
// denied" is one fact about this process.
func TestMergeKeepsThePrimaryCodeAndEveryReasonWhenAllFail(t *testing.T) {
	rows, sources, err := mergedScan(t, "linux", 2, map[string]collectorAnswer{
		"ss":      {},
		"netstat": {stderr: "netstat: permission denied\n", err: collectorExit(1)},
	})
	if len(rows) != 0 || sources != nil {
		t.Fatalf("rows=%+v sources=%v, want nothing published", rows, sources)
	}
	var observed *ObservationError
	if !errors.As(err, &observed) {
		t.Fatalf("error = %v, want the primary's classified failure", err)
	}
	for _, want := range []string{"ss:", "netstat:", "permission denied"} {
		if !strings.Contains(observed.Error(), want) {
			t.Fatalf("detail = %q, want it to contain %q", observed.Error(), want)
		}
	}
}

// A withdrawn observation publishes nothing, even from a rung that had already
// answered: the caller is gone, so the answer belongs to nobody.
func TestMergeStopsWhenTheCallerWithdraws(t *testing.T) {
	rows, sources, err := mergedScan(t, "linux", 2, map[string]collectorAnswer{
		"ss":      {out: ssHeader + ssRow},
		"netstat": {err: context.Canceled},
	})
	if len(rows) != 0 || sources != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("rows=%+v sources=%v err=%v, want the cancellation itself", rows, sources, err)
	}
}

// One collector is today's read, and it names no list: the alternative that
// answered travels in observation.source instead.
func TestSingleSourceScanPublishesNoSourceList(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, sources, err := ScanContextSources(ctx, 1)
	if rows != nil || sources != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("rows=%+v sources=%v err=%v, want today's cancelled read", rows, sources, err)
	}
	if _, sources, _ := ScanContextSources(context.Background(), 1); sources != nil {
		t.Fatalf("sources = %v, want none for a single-collector read", sources)
	}
}

// The published sentence names every contributor and states the consequence, so
// a reader cannot take a port missing from a merged table as free.
func TestMergedSourcesDetailNamesEveryContributor(t *testing.T) {
	if got := MergedSourcesDetail(nil); got != "" {
		t.Fatalf("detail = %q, want nothing for a single-source read", got)
	}
	got := MergedSourcesDetail([]string{"ss", "netstat"})
	for _, want := range []string{"merged from ss, netstat", "unknown, not free"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail = %q, want it to contain %q", got, want)
		}
	}
}
