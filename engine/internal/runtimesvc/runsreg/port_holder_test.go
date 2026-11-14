package runsreg

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestExitRowCarriesThePortHolderAndLeavesPortsEmpty(t *testing.T) {
	group := "shop@other"
	ports := []state.Port{{
		Host: "localhost", Port: 3000, PID: 9, Command: "node server.js", Group: &group,
	}}
	exit := Exit{
		Record:   Record{ID: "run-1", Group: "shop", Name: "api", PortHint: 3000},
		Code:     1,
		Reason:   ReasonPortOccupied,
		ExitedAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
	}
	row := exitRow(exit, ports)
	if row.PortHolder == nil || row.PortHolder.PID != 9 || row.PortHolder.Kind != state.PortHolderListener {
		t.Fatalf("port_holder = %+v, want the listener on the declared port", row.PortHolder)
	}
	if len(row.Ports) != 0 {
		t.Fatalf("ports = %v, want none: the exited run holds no port", row.Ports)
	}
	if row.PortHint == nil || *row.PortHint != 3000 {
		t.Fatalf("port_hint = %v, want 3000", row.PortHint)
	}
}

func TestExitRowDoesNotInventAHolderForOtherFailures(t *testing.T) {
	ports := []state.Port{{Host: "localhost", Port: 3000, PID: 9}}
	exit := Exit{Record: Record{Group: "shop", Name: "api", PortHint: 3000}, Reason: ReasonCrashed}
	if row := exitRow(exit, ports); row.PortHolder != nil {
		t.Fatalf("port_holder = %+v, want none for a crash", row.PortHolder)
	}
}

func TestServiceExitCarriesThePortHint(t *testing.T) {
	exit := Exit{Record: Record{ID: "run-1", PortHint: 3000}, Reason: ReasonPortOccupied}
	if got := exit.ServiceExit(); got.PortHint != 3000 {
		t.Fatalf("port_hint = %d, want 3000", got.PortHint)
	}
}
