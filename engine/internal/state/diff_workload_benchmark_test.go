package state

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"testing"
)

// Dense wire-shaped records and independently allocated snapshots avoid a
// misleading benchmark consisting only of scalar zeros or shared pointers.
func BenchmarkDiffWorkloads(b *testing.B) {
	for _, n := range []int{16, 1024} {
		for _, work := range []string{"idle", "sparse", "dense", "churn", "reordered"} {
			b.Run(fmt.Sprintf("rows=%d/%s", n, work), func(b *testing.B) {
				before, after := diffWorkload(n, work)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = DiffWithStats(before, after)
				}
			})
		}
	}
}

func diffWorkload(n int, work string) (Snapshot, Snapshot) {
	prev := Snapshot{Seq: 1, Ports: make([]Port, n), Groups: []Group{{Host: "localhost", Name: "demo", Status: "running", Members: []int{}, Services: []Service{}, Machine: []MachineRef{}}}, Sessions: []SessionRecord{}}
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	for i := range prev.Ports {
		group := SourceStart
		name := fmt.Sprintf("svc-%04d", i)
		prev.Ports[i] = Port{Host: "localhost", Port: 3000 + i, BindAddress: "127.0.0.1", BindAddresses: []string{"127.0.0.1", "::1"}, PID: 100 + i, PPID: 10, Process: "worker", DisplayName: name, Command: "worker --serve", Cwd: "/work/demo", Name: str(name), Group: str("demo"), GroupSource: &group, ProjectRoot: str("/work/demo"), Type: TypeUser, User: "dev", Stats: &Stats{MemoryRSS: 1024, CPUPercent: 1.5, ThreadCount: 4}, Health: &Health{Status: HealthOK, Code: 200}, Run: &Run{ID: name, Group: "demo", Name: name, RootPID: 100 + i}, StartedAt: str("2026-01-01T00:00:00Z")}
		prev.Groups[0].Members = append(prev.Groups[0].Members, 3000+i)
		prev.Groups[0].Services = append(prev.Groups[0].Services, Service{Name: name, Cmd: "worker --serve", Port: num(3000 + i), PID: num(100 + i), Running: true, PortActual: num(3000 + i), DependsOn: []string{}, RunID: str(name), HealthStatus: &Health{Status: HealthOK, Code: 200}, ManifestHash: str("hash"), LogPath: str("/logs/" + name)})
	}
	var next Snapshot
	data, _ := json.Marshal(prev)
	if err := json.Unmarshal(data, &next); err != nil {
		panic(err)
	}
	next.Seq = 2
	switch work {
	case "sparse":
		next.Ports[n-1].Stats.MemoryRSS++
		next.Groups[0].Services[n-1].Running = false
	case "dense":
		for i := range next.Ports {
			next.Ports[i].Stats.MemoryRSS++
			next.Groups[0].Services[i].Running = false
		}
	case "churn":
		next.Ports = slices.Clone(next.Ports[n/4:])
		for i := 0; i < n/4; i++ {
			next.Ports = append(next.Ports, Port{Host: "localhost", Port: 4000 + n + i, PID: 2000 + n + i})
		}
	case "reordered":
		rand.New(rand.NewSource(42)).Shuffle(len(next.Ports), func(i, j int) { next.Ports[i], next.Ports[j] = next.Ports[j], next.Ports[i] })
	}
	return prev, next
}
