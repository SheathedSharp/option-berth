package killer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

func TestSignalObserverOnlyReportsAcceptedActions(t *testing.T) {
	for _, mode := range []string{"accepted", "dry-run", "cancelled", "denied", "replaced", "docker"} {
		t.Run(mode, func(t *testing.T) {
			m := newMachine(ProcessTable{42: {PID: 42}})
			e := m.engine()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			snapshot := []ports.ListeningPort{listener(3000, 42)}
			var receipts []int
			opts := Options{Force: true, OnSignal: func(pid int) {
				if len(m.signals) == 0 {
					t.Fatal("receipt preceded signal")
				}
				receipts = append(receipts, pid)
			}}
			switch mode {
			case "dry-run":
				opts.DryRun = true
			case "cancelled":
				cancel()
			case "denied":
				m.process(42).refuses = errors.New("denied")
			case "replaced":
				reads := int64(0)
				e.startTime = func(int) (time.Time, bool) { reads++; return time.Unix(reads*3600, 0), true }
			case "docker":
				snapshot[0].Type = ports.PortTypeDocker
				snapshot[0].DockerContainer = "synthetic-container"
			}
			e.kill(ctx, snapshot, []Target{{Port: 3000}}, opts)
			if mode == "accepted" {
				if !reflect.DeepEqual(receipts, []int{42}) {
					t.Fatalf("receipts=%v", receipts)
				}
			} else if len(receipts) != 0 {
				t.Fatalf("%s fabricated signal acceptance: %v", mode, receipts)
			}
		})
	}
}

func TestSignalObserverSurvivesCancellationAfterAcceptance(t *testing.T) {
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.ports[3000] = portOpen
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.react = func(*machine, signalCall) { cancel() }
	var receipts []int
	rows := m.engine().kill(ctx, []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}},
		Options{OnSignal: func(pid int) { receipts = append(receipts, pid) }})
	if len(m.signals) != 1 || !reflect.DeepEqual(receipts, []int{42}) || len(rows) != 1 || rows[0].OK {
		t.Fatalf("accepted signal lost when final row changed: signals=%v receipts=%v rows=%+v", m.signals, receipts, rows)
	}
}

func TestSignalObserverUsesRootAcrossAdaptersAndEscalation(t *testing.T) {
	for _, mode := range []string{"process", "group", "native-tree", "manual-tree-partial"} {
		t.Run(mode, func(t *testing.T) {
			m := newMachine(ProcessTable{42: {PID: 42}, 43: {PID: 43, PPID: 42}})
			m.ports[3000] = portOpen
			if mode == "group" {
				m.process(42).group = 42
			}
			e := m.engine()
			e.nativeTree = mode == "native-tree"
			var receipts []int
			opts := Options{Tree: mode == "native-tree", Grace: pollInterval,
				OnSignal: func(pid int) { receipts = append(receipts, pid) }}
			if mode == "manual-tree-partial" {
				opts.Tree, opts.Force = true, true
				m.process(42).refuses = errors.New("root denied")
			}
			e.kill(context.Background(), []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}}, opts)
			want := []int{42, 42}
			if mode == "manual-tree-partial" {
				want = []int{42}
			}
			if !reflect.DeepEqual(receipts, want) {
				t.Fatalf("receipts=%v want=%v", receipts, want)
			}
		})
	}
}

func TestSignalObserverDoesNotInventFailedEscalation(t *testing.T) {
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.ports[3000] = portOpen
	m.react = func(m *machine, c signalCall) {
		m.process(c.pid).refuses = errors.New("escalation denied")
	}
	var receipts []int
	rows := m.engine().kill(context.Background(), []ports.ListeningPort{listener(3000, 42)},
		[]Target{{Port: 3000}}, Options{Grace: pollInterval, OnSignal: func(pid int) { receipts = append(receipts, pid) }})
	if len(m.signals) != 2 || !reflect.DeepEqual(receipts, []int{42}) || rows[0].OK {
		t.Fatalf("receipts=%v signals=%v rows=%+v", receipts, m.signals, rows)
	}
}
