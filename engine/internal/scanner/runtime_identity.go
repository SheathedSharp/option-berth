package scanner

import "github.com/sheathedsharp/option-berth/internal/state"

// Optional observations belong to a runtime, not just a socket address. A
// port reused after restart must not inherit the previous run's health/stats.
func sameRuntime(a, b state.Port) bool {
	if a.PID != b.PID || stringValue(a.StartedAt) != stringValue(b.StartedAt) {
		return false
	}
	container := func(p state.Port) string {
		if p.Docker != nil {
			return p.Docker.Container
		}
		return ""
	}
	if container(a) != container(b) || (a.PID <= 0 && container(a) == "") {
		return false
	}
	run := func(p state.Port) string {
		if p.Run != nil {
			return p.Run.ID
		}
		return ""
	}
	return run(a) == run(b)
}

func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
