package runsreg

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/servicefacts"
)

func TestServiceFactsRemainDetachedAcrossExitAndRestart(t *testing.T) {
	r := testRegistry(1, 2)
	r.Alive = func(int) bool { t.Fatal("capture must not probe processes"); return false }
	key := servicefacts.Key{Group: "shop", Service: "api"}
	base := time.Unix(1, 0)
	r.Register(Record{ID: "old", PID: 1, Group: key.Group, Name: key.Service, PortHint: 8100, StartedAt: base})
	before := r.ServiceFacts()
	r.Stopping([]int{1})
	stopping := r.ServiceFacts()
	if !stopping[key].Stopping || stopping[key].LastExit != nil {
		t.Fatalf("stopping facts = %+v", stopping[key])
	}
	r.Exited(1, 0, true)
	finished := r.ServiceFacts()
	if finished[key].Run != nil || finished[key].LastExit == nil || finished[key].LastExit.RunID != "old" {
		t.Fatalf("finished facts = %+v", finished[key])
	}
	r.Register(Record{ID: "new", PID: 2, Group: key.Group, Name: key.Service, PortHint: 8200, StartedAt: base.Add(time.Second)})
	current := r.ServiceFacts()
	if current[key].Run.ID != "new" || current[key].PortHint != 8200 || current[key].LastExit != nil {
		t.Fatalf("current facts = %+v", current[key])
	}
	if before[key].Run.ID != "old" || before[key].Stopping || before[key].PortHint != 8100 || before[key].LastExit != nil {
		t.Fatalf("previous snapshot changed: %+v", before[key])
	}
	before[key].Run.Cmd = "modified snapshot"
	finished[key].LastExit.Reason = "modified snapshot"
	if got := r.ServiceFacts()[key]; got.Run.Cmd != "" {
		t.Fatal("snapshot mutation leaked into registry")
	}
	if got, _ := r.LatestExit(key.Group, key.Service); got.Reason == "modified snapshot" {
		t.Fatal("snapshot mutation leaked into exit history")
	}
}

func TestServiceFactsKeepSourceTimestampPrecision(t *testing.T) {
	r := testRegistry(1, 2)
	base := time.Unix(10, 0)
	r.Register(Record{ID: "z-old", PID: 1, Group: "shop", Name: "api", PortHint: 8001, StartedAt: base})
	r.Register(Record{ID: "a-new", PID: 2, Group: "shop", Name: "api", PortHint: 8002, StartedAt: base.Add(time.Nanosecond)})
	got := r.ServiceFacts()[servicefacts.Key{Group: "shop", Service: "api"}]
	if got.Run == nil || got.Run.ID != "a-new" || got.PortHint != 8002 || got.Run.StartedAt != base.UTC().Format(time.RFC3339) {
		t.Fatalf("selection must retain source precision but preserve wire timestamp: %+v", got)
	}
}

func TestServiceFactsConcurrentLifecycle(t *testing.T) {
	r := testRegistry(1)
	key := servicefacts.Key{Group: "shop", Service: "worker"}
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; i < 100; i++ {
			r.Register(Record{ID: fmt.Sprintf("run-%d", i), PID: 1, Group: key.Group, Name: key.Service})
			r.Exited(1, 0, false)
		}
	}()
	defer writer.Wait()
	for i := 0; i < 100; i++ {
		got := r.ServiceFacts()[key]
		if got.Run != nil && got.LastExit != nil {
			t.Fatalf("mixed live and exited generations: %+v", got)
		}
	}
}
