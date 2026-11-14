package groupstart

// dependencyPendingDisplay adapts the shared verdict without changing the
// dependency timeout diagnostic already consumed by CLI callers.
func dependencyPendingDisplay(name, reason string, hasPort bool) string {
	if !hasPort && reason == "not_running" {
		return name + " to be running (no port)"
	}
	return name + " (" + reason + ")"
}
