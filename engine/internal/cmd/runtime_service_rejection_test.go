package cmd

import (
	"strings"
	"testing"
)

func TestRetiredRuntimeEntrypointsAreRejectedNotRouted(t *testing.T) {
	for _, args := range [][]string{{"daemon", "stop"}, {"serve", "--detach"}, {"--no-color", "daemon", "status"}, {"--no-daemon", "status"}} {
		err := rejectRetiredRuntimeCommand(args)
		if err == nil || !strings.Contains(err.Error(), "runtime") {
			t.Fatalf("missing migration refusal for %v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"runtime", "service", "status"}, {"start", "daemon"}, {"start", "--", "--no-daemon"}} {
		if err := rejectRetiredRuntimeCommand(args); err != nil {
			t.Fatalf("rejected unrelated argument %v: %v", args, err)
		}
	}
}
