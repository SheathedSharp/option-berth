package runsreg

import "testing"

func TestOccupiedExitRecognizesNativeBindDiagnostics(t *testing.T) {
	for _, message := range []string{
		"OSError: [Errno 48] Address already in use",
		"OSError: [Errno 98] Address in use",
		"listen EADDRINUSE: address already in use :::8000",
	} {
		if got := occupiedExit(ReasonCrashed, []string{message}); got != ReasonPortOccupied {
			t.Fatalf("%q classified as %q", message, got)
		}
		if got := occupiedExit(ReasonStopped, []string{message}); got != ReasonStopped {
			t.Fatalf("stop cause overwritten: %q", got)
		}
	}
	if got := occupiedExit(ReasonCrashed, []string{"invalid address", "unrelated error"}); got != ReasonCrashed {
		t.Fatalf("unrelated failure classified as %q", got)
	}
}
