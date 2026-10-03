package groupstart

import (
	"errors"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
)

func TestDependencyWaitDoesNotReuseIdleObservation(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "name: active-wait\nservices:\n  - name: api\n    cmd: serve\n    port: 8080\n")
	now := time.Now()
	bound := false
	l := scanner.New(scanner.Options{Now: func() time.Time { return now }, Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		if !bound {
			return nil, nil
		}
		return []ports.ListeningPort{{Port: 8080, PID: 42, Cwd: dir}}, nil
	}})
	rt := &daemon.Runtime{Scanner: l}
	if _, err := l.Snapshot(scanner.Include{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(scanner.MinScanInterval)
	bound = true
	deps := []groups.Service{{Name: "api", Port: 8080}}
	if missing := pending(rt, "active-wait", deps, map[string]int{"api": 8080}); len(missing) != 0 {
		t.Fatalf("active dependency hidden by idle TTL: %v", missing)
	}
}

func TestDependencyWaitDoesNotAcceptLastGoodAfterCollectorFailure(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "name: active-wait\nservices:\n  - name: api\n    cmd: serve\n    port: 8080\n")
	now := time.Now()
	fail := false
	l := scanner.New(scanner.Options{Now: func() time.Time { return now }, Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		if fail {
			return nil, errors.New("collector unavailable")
		}
		return []ports.ListeningPort{{Port: 8080, PID: 42, Cwd: dir}}, nil
	}})
	rt := &daemon.Runtime{Scanner: l}
	if _, err := l.Snapshot(scanner.Include{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(scanner.CacheTTL)
	fail = true
	deps := []groups.Service{{Name: "api", Port: 8080}}
	if missing := pending(rt, "active-wait", deps, map[string]int{"api": 8080}); len(missing) == 0 {
		t.Fatal("collector failure was accepted as current dependency readiness")
	}
}
