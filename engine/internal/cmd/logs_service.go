package cmd

import "github.com/sheathedsharp/option-berth/internal/state"

// A declared path alone is not proof that an observed listener writes there.
// RunID binds a listening service to a managed run; absent that evidence, the
// existing process-log path remains authoritative. Portless/stopped services
// retain their existing file-log fallback.
func preferredServiceLog(service state.Service) string {
	if service.LogPath == nil || *service.LogPath == "" {
		return ""
	}
	if service.PortActual == nil || (service.RunID != nil && *service.RunID != "") {
		return *service.LogPath
	}
	return ""
}
