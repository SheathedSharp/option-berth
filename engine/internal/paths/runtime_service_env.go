package paths

import "fmt"

// RejectRetiredRuntimeServiceEnvironment refuses old routing/guard variables
// instead of silently ignoring them and starting a different host. Values are
// deliberately absent from diagnostics: an override may contain private data.
func RejectRetiredRuntimeServiceEnvironment(getenv func(string) string) error {
	for _, item := range [][2]string{
		{"BERTH_NO_AUTOSTART", "BERTH_RUNTIME_SERVICE_NO_AUTOSTART"},
		{"BERTH_SOCKET", "BERTH_RUNTIME_SERVICE_SOCKET"},
		{"BERTH_DAEMON_DETACHED", "BERTH_RUNTIME_SERVICE_DETACHED"},
		{"BERTH_ALLOW_TEST_DAEMON", "BERTH_RUNTIME_SERVICE_ALLOW_TEST_BINARY"},
	} {
		if getenv(item[0]) != "" {
			return fmt.Errorf("%s was removed in runtime service protocol 3; unset it and use %s; stop any previous host explicitly before switching", item[0], item[1])
		}
	}
	return nil
}
