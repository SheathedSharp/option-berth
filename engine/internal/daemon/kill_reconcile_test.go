package daemon

import (
	"context"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
)

type postStopRegistry struct {
	noRuns
	pruned bool
}

func (r *postStopRegistry) Prune() { r.pruned = true }

func TestAfterKillReconcilesRunsBeforePublishing(t *testing.T) {
	registry := &postStopRegistry{}
	rt := &Runtime{}
	rt.SetRuns(registry)
	scans := 0
	rt.Scanner = scanner.New(scanner.Options{ScanContext: func(context.Context, scanner.Include) ([]ports.ListeningPort, error) {
		scans++
		if !registry.pruned {
			t.Error("post-stop snapshot observed unreconciled imported runs")
		}
		return nil, nil
	}})
	if _, err := afterKill(&Request{Runtime: rt}, true); err != nil {
		t.Fatal(err)
	}
	if registry.pruned || scans != 0 {
		t.Fatal("dry run performed reconciliation")
	}
	if _, err := afterKill(&Request{Runtime: rt}, false); err != nil {
		t.Fatal(err)
	}
	if !registry.pruned || scans != 1 {
		t.Fatalf("pruned=%v scans=%d", registry.pruned, scans)
	}
}
