package scanner

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// A merged scan publishes every collector that contributed, and its absence
// stays unknown: a port the merge did not see could be missing from any one of
// the tables that went into it.
func TestMergedScanPublishesEveryContributorAndStaysNarrow(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	writeConfig(t, dir, `name: shop
services: []
machine:
  - name: redis
    port: 6379
`)
	l := New(Options{
		RuntimeServiceVersion: "test",
		Store:                 st,
		ScanSources: func(context.Context, Include) (ports.Read, error) {
			return ports.Read{
				Rows:    []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3", Cwd: dir}},
				Sources: []string{"ss", "netstat"},
			}, nil
		},
	})

	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got := snap.Observation
	if got == nil || got.State != state.ObservationObservable {
		t.Fatalf("observation = %+v, want an observable merged read", got)
	}
	if want := []string{"ss", "netstat"}; !reflect.DeepEqual(got.Sources, want) {
		t.Fatalf("sources = %v, want %v", got.Sources, want)
	}
	if got.Source != "" {
		t.Fatalf("source = %q, want no single source for a merged table", got.Source)
	}
	if !strings.Contains(got.Detail, "merged from ss, netstat") {
		t.Fatalf("detail = %q, want the contributors named", got.Detail)
	}
	var group *state.Group
	for i := range snap.Groups {
		if snap.Groups[i].Name == "shop" {
			group = &snap.Groups[i]
			break
		}
	}
	if group == nil || len(group.Machine) != 1 {
		t.Fatalf("groups = %+v, want the shop manifest with one machine reference", snap.Groups)
	}
	if group.Machine[0].Listening || group.Machine[0].ListeningKnown {
		t.Fatalf("machine = %+v, want an absent port to be unknown under a merged table", group.Machine[0])
	}
}

// The runtime service is where a merge happens, so it is where a row's
// collector has to reach the wire: a merged row published without its rung
// arrives at every client anonymous, and no reader below can recover it.
func TestMergedScanPublishesTheCollectorOnEachRow(t *testing.T) {
	l := New(Options{
		RuntimeServiceVersion: "test",
		ScanSources: func(context.Context, Include) (ports.Read, error) {
			return ports.Read{
				Rows: []ports.ListeningPort{
					{Port: 18121, PID: 41, BindAddress: "127.0.0.1", IPVersion: "IPv4", Source: "ss"},
					{Port: 18122, PID: 42, BindAddress: "127.0.0.1", IPVersion: "IPv4", Source: "netstat"},
				},
				Sources: []string{"ss", "netstat"},
			}, nil
		},
	})

	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got := map[int]string{}
	for _, row := range snap.Ports {
		got[row.Port] = row.Source
	}
	if want := map[int]string{18121: "ss", 18122: "netstat"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("published row sources = %v, want %v", got, want)
	}
}

// The merged seam wins over the single-source one, so a loop configured for
// several collectors cannot silently publish one table as if it were the merge.
func TestScanSourcesTakesPrecedenceOverScanSource(t *testing.T) {
	l := New(Options{
		ScanSource: func(context.Context, Include) (ports.Read, error) {
			t.Error("the single-source seam answered a merge-configured loop")
			return ports.Read{Source: "netstat"}, nil
		},
		ScanSources: func(context.Context, Include) (ports.Read, error) {
			return ports.Read{
				Rows:    []ports.ListeningPort{{Port: 8123, PID: 42, BindAddress: "127.0.0.1"}},
				Sources: []string{"ss", "netstat"},
			}, nil
		},
	})
	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Observation == nil || len(snap.Observation.Sources) != 2 {
		t.Fatalf("observation = %+v, want the merged read", snap.Observation)
	}
}

// A loop configured for several collectors installs the merged production seam;
// the default installs the single-source one, which is the read this product has
// always done.
func TestSourceLimitChoosesTheProductionCollector(t *testing.T) {
	merged := New(Options{SourceLimit: 2})
	if merged.opts.ScanSources == nil || merged.opts.ScanSource != nil {
		t.Fatalf("scanSources=%v scanSource=%v, want the merged seam",
			merged.opts.ScanSources != nil, merged.opts.ScanSource != nil)
	}
	single := New(Options{})
	if single.opts.ScanSource == nil || single.opts.ScanSources != nil {
		t.Fatalf("scanSources=%v scanSource=%v, want the single-source seam",
			single.opts.ScanSources != nil, single.opts.ScanSource != nil)
	}
}

// The degradation times answer "how long has this been going on", which no
// single scan can: the first unhealthy verdict starts the clock, the next whole
// read hands it to recovered_at, and a re-check publishes neither a frame nor a
// new time.
func TestDegradationTimesFollowThePublishedVerdict(t *testing.T) {
	rows := []ports.ListeningPort{{Port: 18001, PID: 123, BindAddress: "127.0.0.1"}}
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	merged := true
	l := New(Options{
		Now: func() time.Time { return now },
		ScanSources: func(context.Context, Include) (ports.Read, error) {
			if merged {
				return ports.Read{Rows: rows, Sources: []string{"ss", "netstat"}}, nil
			}
			return ports.Read{Rows: rows}, nil
		},
	})

	degraded, _, changed, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want the merged read published", changed, err)
	}
	if got := degraded.Observation; got == nil || got.DegradedSince == "" || got.RecoveredAt != "" {
		t.Fatalf("observation = %+v, want the degradation started and nothing recovered yet", got)
	}
	started := degraded.Observation.DegradedSince

	now = now.Add(30 * time.Second)
	merged = false
	recovered, _, changed, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want the recovery published", changed, err)
	}
	if got := recovered.Observation; got == nil || got.RecoveredAt == "" || got.DegradedSince != "" {
		t.Fatalf("observation = %+v, want the recovery recorded and the degradation cleared", got)
	}
	if started == recovered.Observation.RecoveredAt {
		t.Fatal("the recovery was stamped with the moment the degradation began")
	}

	now = now.Add(30 * time.Second)
	again, _, changed, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil {
		t.Fatalf("third scan: %v", err)
	}
	if changed {
		t.Fatal("a re-checked healthy process published another frame")
	}
	if got := again.Observation; got == nil || got.RecoveredAt != recovered.Observation.RecoveredAt {
		t.Fatalf("observation = %+v, want the recovery time kept, not re-stamped", got)
	}
}

// A collector that failed publishes no verdict at all, so it does not move the
// degradation clock: keeping the last snapshot and claiming a new degradation
// began would describe a read that never happened.
func TestFailedScanKeepsTheDegradationClock(t *testing.T) {
	rows := []ports.ListeningPort{{Port: 18001, PID: 123, BindAddress: "127.0.0.1"}}
	failing := false
	l := New(Options{
		ScanSource: func(context.Context, Include) (ports.Read, error) {
			if failing {
				return ports.Read{Source: "lsof"}, &ports.ObservationError{
					Code: ports.CodeObservationIncomplete, Detail: "lsof: permission denied",
					Hint: "run doctor",
				}
			}
			return ports.Read{Rows: rows, Source: "lsof"}, nil
		},
	})

	first, _, _, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil || first.Observation == nil || first.Observation.DegradedSince == "" {
		t.Fatalf("first scan = %+v, %v", first.Observation, err)
	}
	degradedSince := first.Observation.DegradedSince

	failing = true
	if _, _, changed, err := l.scanLocked(Include{}, Include{}, false); err == nil || changed {
		t.Fatalf("changed=%v err=%v, want the failure to keep the last snapshot", changed, err)
	}
	if got := l.Cached().Observation; got == nil || got.DegradedSince != degradedSince {
		t.Fatalf("observation = %+v, want the same degradation still open", got)
	}
}

// A merged read whose primary pass was partial keeps that verdict: the union
// has readable rows, but the unread part of one rung's table is still unknown.
func TestMergedPartialReadPublishesThePartialVerdict(t *testing.T) {
	partial := &ports.ObservationError{
		Code: ports.CodeObservationIncomplete, Detail: "netstat: unreadable row",
		Hint: "run `oberth doctor --only listener_observation`", Partial: true,
		Rows: []ports.ListeningPort{{Port: 18120, PID: 42, BindAddress: "127.0.0.1", IPVersion: "IPv4"}},
	}
	l := New(Options{
		RuntimeServiceVersion: "test",
		SourceLimit:           2,
		ScanSources: func(context.Context, Include) (ports.Read, error) {
			return ports.Read{Rows: partial.Rows, Sources: []string{"netstat", "lsof"}}, partial
		},
	})

	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got := snap.Observation
	if got == nil || got.State != state.ObservationPartial {
		t.Fatalf("observation = %+v, want the partial verdict", got)
	}
	if want := []string{"netstat", "lsof"}; !reflect.DeepEqual(got.Sources, want) {
		t.Fatalf("sources = %v, want %v", got.Sources, want)
	}
	if len(snap.Ports) != 1 || snap.Ports[0].Port != 18120 {
		t.Fatalf("ports = %+v, want the readable row", snap.Ports)
	}
}
