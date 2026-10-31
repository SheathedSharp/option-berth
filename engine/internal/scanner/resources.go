package scanner

import (
	"context"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// FillServiceStatsContext gives a running worker without a listener the same
// cpu/memory sample a listener row gets. It is the other half of ports.Enrich:
// a service's stats used to exist only if the service owned a port, so a
// portless worker was invisible to every resource read.
//
// Only running services with no listener and no existing sample are filled.
// A listener's process is already measured with its port row, and a stopped
// service has no live process to sample. A missing sample stays nil: unknown
// is not zero.
func FillServiceStatsContext(ctx context.Context, groups []state.Group, sample func([]int) map[int]ports.ProcSample) {
	pids := make([]int, 0, len(groups))
	for i := range groups {
		for j := range groups[i].Services {
			svc := &groups[i].Services[j]
			if !needsProcessSample(svc) {
				continue
			}
			pids = append(pids, *svc.PID)
		}
	}
	if len(pids) == 0 || ctx.Err() != nil {
		return
	}

	var samples map[int]ports.ProcSample
	if sample != nil {
		samples = sample(pids)
	} else {
		samples = ports.SampleProcStatsContext(ctx, pids)
	}
	if len(samples) == 0 || ctx.Err() != nil {
		return
	}

	for i := range groups {
		for j := range groups[i].Services {
			svc := &groups[i].Services[j]
			if !needsProcessSample(svc) {
				continue
			}
			s, ok := samples[*svc.PID]
			if !ok {
				continue
			}
			// A recycled pid must not inherit a recorded run's identity. The
			// sample carries the process start; the run carries its own.
			if !sameProcessStart(valueOrEmpty(svc.StartedAt), s.StartedAt) {
				continue
			}
			svc.Stats = &state.Stats{
				CPUPercent:  s.CPUPercent,
				MemoryRSS:   s.MemoryRSS,
				ThreadCount: s.ThreadCount,
				Uptime:      s.Uptime,
				State:       s.State,
			}
		}
	}
}

// needsProcessSample is the population FillServiceStatsContext owns: a
// running service with a pid, no listener (the port row is measured already)
// and no sample yet.
func needsProcessSample(svc *state.Service) bool {
	return svc != nil && svc.Running && svc.Stats == nil && svc.PortActual == nil &&
		svc.PID != nil && *svc.PID > 0
}

// sameProcessStart compares a run's recorded start with the sampled process
// start. A missing value on either side is unknown, not a contradiction, so
// the sample is kept; a known mismatch is the recycled-pid case and is
// rejected.
func sameProcessStart(recorded, sampled string) bool {
	if recorded == "" || sampled == "" {
		return true
	}
	left, err := time.Parse(time.RFC3339, recorded)
	if err != nil {
		return true
	}
	right, err := time.Parse(time.RFC3339, sampled)
	if err != nil {
		return true
	}
	delta := left.Sub(right)
	if delta < 0 {
		delta = -delta
	}
	return delta <= ports.StartTolerance
}

func valueOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
