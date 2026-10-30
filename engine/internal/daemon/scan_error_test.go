package daemon

import (
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// A failed observation crosses the RPC boundary with the collector's code and
// hint, so a sandboxed CLI does not have to parse "internal" or the detail.
func TestScanFailureKeepsObservationCodes(t *testing.T) {
	for _, tt := range []struct {
		collector string
		rpcCode   int
	}{
		{ports.CodeObservationEmpty, rpc.CodeObservationEmpty},
		{ports.CodeObservationUnrecognized, rpc.CodeObservationUnrecognized},
		{ports.CodeObservationIncomplete, rpc.CodeObservationIncomplete},
		{ports.CodeObservationBlind, rpc.CodeObservationBlind},
		{ports.CodeObservationBindDenied, rpc.CodeObservationBindDenied},
	} {
		err := &ports.ObservationError{
			Code:   tt.collector,
			Detail: "netstat: exited 0 but printed no listening table",
			Hint:   "retry outside the sandbox",
		}
		got := scanFailure(err)
		if got.Data.Code != tt.collector || got.Code != tt.rpcCode {
			t.Errorf("%s: got %d/%q, want %d/%q", tt.collector, got.Code, got.Data.Code, tt.rpcCode, tt.collector)
		}
		if got.Data.Hint != err.Hint || got.Data.Detail != err.Detail {
			t.Errorf("%s: lost detail or hint: %+v", tt.collector, got.Data)
		}
	}
}

// Anything else is still an engine failure, not a fake observation.
func TestScanFailureFallsBackToInternal(t *testing.T) {
	got := scanFailure(errUnexpected{})
	if got.Data.Code != "internal" || !strings.Contains(got.Data.Detail, "boom") {
		t.Fatalf("got %+v", got.Data)
	}
}

type errUnexpected struct{}

func (errUnexpected) Error() string { return "boom" }
