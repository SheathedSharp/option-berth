package runsreg

import (
	"fmt"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// BenchmarkGroupsWithRegistry includes the registry reads omitted by the
// listener-only join benchmark. Half the services have exited; the live half
// mixes listeners and portless workers. No OS processes or daemon are started.
func BenchmarkGroupsWithRegistry(b *testing.B) {
	for _, n := range []int{32, 256, 1024} {
		b.Run(fmt.Sprintf("services=%d", n), func(b *testing.B) {
			reg := New()
			reg.Mirror = false
			group := "bench"
			cfg := &groups.Config{Name: group, Dir: "/nonexistent/oberth-runtime-bench"}
			var listeners []state.Port
			for i := 0; i < n; i++ {
				name := fmt.Sprintf("service-%d", i)
				svc := groups.Service{Name: name, Cmd: "worker"}
				rec := Record{
					ID: name, PID: i + 1, Group: group, Name: name,
					Cmd: svc.Cmd, Cwd: cfg.Dir, StartedAt: time.Unix(1, 0),
				}
				if i%2 == 0 {
					if i%4 == 0 {
						svc.PortAuto = true
						rec.PortHint = 3000 + i
						listeners = append(listeners, state.Port{
							Port: rec.PortHint, PID: rec.PID, Group: &group,
							Run: &state.Run{ID: rec.ID, Name: name, Group: group, RootPID: rec.PID},
						})
					}
					reg.runs[rec.PID] = rec
				} else {
					reg.exits = append(reg.exits, Exit{Record: rec, Code: 1, Reason: ReasonCrashed, ExitedAt: time.Unix(2, 0)})
				}
				cfg.Services = append(cfg.Services, svc)
			}
			index := groups.NewIndex()
			index.Add(cfg)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = groups.GroupsWith(listeners, index, reg)
			}
		})
	}
}
