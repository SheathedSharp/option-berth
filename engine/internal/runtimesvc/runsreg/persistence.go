package runsreg

import "log/slog"

// SetLogger attaches diagnostics, not another persistence owner. Call at startup.
func (r *Registry) SetLogger(logger *slog.Logger) {
	r.mu.Lock()
	r.logger = logger
	r.mu.Unlock()
}

// Runtime mirrors are compatibility projections of observed process facts.
// Failure cannot undo a process exit or make a started child disappear. Report
// it instead of silently treating memory as durable. No unbounded retry queue,
// second registry, or detached worker is introduced. Logs stay local; no run
// commands, environment values, log tails, or record payloads are added here.
// Like the existing disk operation, a synchronous log sink must return.
func (r *Registry) persistenceResult(operation string, err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	logger := r.logger
	r.mu.Unlock()
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("run persistence failed; in-memory facts are not confirmed durable",
		"operation", operation, "error", err)
}
