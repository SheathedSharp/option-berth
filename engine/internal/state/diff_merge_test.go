package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"
)

func TestAdaptiveDiffMatchesOracle(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		prev, next := generatedTransition(rng)
		for _, ordered := range []bool{false, true} {
			if ordered {
				slices.SortFunc(prev.Ports, orderPortsForTest)
				slices.SortFunc(next.Ports, orderPortsForTest)
			}
			for _, stats := range []bool{false, true} {
				beforePrev, err := json.Marshal(prev)
				if err != nil {
					t.Fatal(err)
				}
				beforeNext, err := json.Marshal(next)
				if err != nil {
					t.Fatal(err)
				}
				got, want := diff(prev, next, stats), oracleDiffInternal(prev, next, stats)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("seed=%d ordered=%v stats=%v\ngot=%+v\nwant=%+v", seed, ordered, stats, got, want)
				}
				afterPrev, err := json.Marshal(prev)
				if err != nil {
					t.Fatal(err)
				}
				afterNext, err := json.Marshal(next)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(afterPrev, beforePrev) || !bytes.Equal(afterNext, beforeNext) {
					t.Fatal("input changed")
				}
			}
		}
	}
}

func generatedTransition(rng *rand.Rand) (Snapshot, Snapshot) {
	prev := Snapshot{Seq: 1, At: "before"}
	next := Snapshot{Seq: 2, At: "after"}
	hosts := []string{"", "localhost", "remote", "x/y"}
	for i := 0; i < rng.Intn(50)+1; i++ {
		p := Port{Host: hosts[rng.Intn(len(hosts))], Port: 3000 + rng.Intn(20), BindAddress: []string{"::1", "127.0.0.1"}[rng.Intn(2)], PID: rng.Intn(5), DisplayName: fmt.Sprint(i), Stats: &Stats{MemoryRSS: int64(i)}, Health: &Health{Status: HealthOK, Code: 200}}
		prev.Ports = append(prev.Ports, p)
		if rng.Intn(4) == 0 {
			continue
		}
		switch rng.Intn(4) {
		case 0:
			p.PID++
		case 1:
			p.DisplayName = "renamed"
		case 2:
			p.Stats = &Stats{MemoryRSS: 999}
		}
		next.Ports = append(next.Ports, p)
	}
	if rng.Intn(2) == 0 {
		next.Ports = append(next.Ports, Port{Port: 8080, PID: 42})
	}
	for i := 0; i < 10; i++ {
		g := Group{Name: fmt.Sprint(rng.Intn(5)), Host: hosts[rng.Intn(len(hosts))], Status: "running", Services: []Service{{Name: "api", Cmd: "serve"}}}
		prev.Groups = append(prev.Groups, g)
		if rng.Intn(3) > 0 {
			g.Branch = fmt.Sprint(rng.Intn(2))
			next.Groups = append(next.Groups, g)
		}
		s := SessionRecord{Session: Session{ID: fmt.Sprint(i)}, Active: true}
		prev.Sessions = append(prev.Sessions, s)
		if rng.Intn(3) > 0 {
			s.Ports = rng.Intn(3)
			next.Sessions = append(next.Sessions, s)
		}
	}
	rng.Shuffle(len(next.Ports), func(i, j int) { next.Ports[i], next.Ports[j] = next.Ports[j], next.Ports[i] })
	return prev, next
}

func TestOrderedDiffRestartRemovalsPrecedeVanished(t *testing.T) {
	prev := Snapshot{Ports: []Port{{Port: 1, PID: 10}, {Port: 2, PID: 20}, {Port: 3, PID: 30}}}
	next := Snapshot{Ports: []Port{{Port: 2, PID: 21}}}
	d := Diff(prev, next)
	if !slices.Equal(d.Ports.Removed, []string{"2:", "1:", "3:"}) {
		t.Fatalf("removal order: %v", d.Ports.Removed)
	}
}

func TestOrderedUnchangedDiffAllocatesNothing(t *testing.T) {
	a := Snapshot{Ports: make([]Port, 1024)}
	for i := range a.Ports {
		a.Ports[i] = Port{Port: i + 1, PID: i + 2}
	}
	b := a
	b.Ports = slices.Clone(a.Ports)
	if got := testing.AllocsPerRun(100, func() { _ = Diff(a, b) }); got != 0 {
		t.Fatalf("idle delta allocated %g times", got)
	}
}

func FuzzAdaptiveDiff(f *testing.F) {
	for _, seed := range []int64{0, 1, 2, 42, 199} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed int64) {
		prev, next := generatedTransition(rand.New(rand.NewSource(seed)))
		for _, ordered := range []bool{false, true} {
			if ordered {
				slices.SortFunc(prev.Ports, orderPortsForTest)
				slices.SortFunc(next.Ports, orderPortsForTest)
			}
			for _, stats := range []bool{false, true} {
				if got, want := diff(prev, next, stats), oracleDiffInternal(prev, next, stats); !reflect.DeepEqual(got, want) {
					t.Fatalf("ordered=%v stats=%v: mismatched delta", ordered, stats)
				}
			}
		}
	})
}

func orderPortsForTest(a, b Port) int { return comparePortIdentity(&a, &b) }
