package paths

import (
	"strings"
	"testing"
)

func TestRetiredRuntimeServiceEnvironmentIsRejected(t *testing.T) {
	for _, key := range []string{"BERTH_SOCKET", "BERTH_DAEMON_DETACHED", "BERTH_ALLOW_TEST_DAEMON", "BERTH_NO_AUTOSTART"} {
		t.Run(key, func(t *testing.T) {
			err := RejectRetiredRuntimeServiceEnvironment(func(name string) string {
				if name == key {
					return "private-value-not-for-output"
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "private-value-not-for-output") {
				t.Fatalf("unsafe or missing refusal: %v", err)
			}
		})
	}
	if err := RejectRetiredRuntimeServiceEnvironment(func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}
