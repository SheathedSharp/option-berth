package ports

import (
	"context"
	"fmt"
	"net"
	"os"
)

// ObservationState is what the self-probe establishes about this process. It
// is a fact produced by binding a real socket and looking for it with the same
// collector a status read uses — not a guess from environment variables.
type ObservationState string

const (
	// ObservationObservable means the collector saw the process's own listener.
	ObservationObservable ObservationState = "observable"
	// ObservationBlind means the process could bind, but the collector did not
	// show that listener (or could not see its owner). An empty table is not
	// evidence that nothing listens.
	ObservationBlind ObservationState = "blind"
	// ObservationBindDenied means the process could not bind a local port, so
	// services started from it cannot listen either.
	ObservationBindDenied ObservationState = "bind_denied"
)

// ProbeResult is the self-probe's verdict plus the detail and next step a
// reader needs. Err is the collector's failure when the probe could not run.
type ProbeResult struct {
	State  ObservationState
	Code   string
	Detail string
	Hint   string
	Err    error
	// Source names the collector that answered when it was not the platform's
	// primary one ("lsof" on Darwin). Empty means the primary collector.
	Source string
}

// ProbeListenerObservation binds 127.0.0.1:0 and asks the same collector a
// status read uses whether it can see that socket as this process. It is the
// only reliable way to tell "nothing is listening" from "this process is not
// allowed to look": sandboxes deny the syscall without denying exec, so the
// command exits 0 with a valid-looking empty table.
//
// Source is set when a Darwin fallback collector answered (listenerSourceLsofName):
// the process can see its own listeners, but the table it read is narrower than
// the primary one, and a reader deciding that a *foreign* port is absent needs
// to know that. It never changes the verdict — the probe only ever asks about a
// socket this process owns.
func ProbeListenerObservation(ctx context.Context) ProbeResult {
	return probeListenerObservationSource(ctx, func(ctx context.Context, _, _ int) ([]ListeningPort, string, error) {
		return scanContextSource(ctx)
	})
}

// probeListenerObservation keeps the verdict seam for the tests that inject a
// scan which cannot see or cannot own the socket.
func probeListenerObservation(ctx context.Context, scan func(context.Context, int, int) ([]ListeningPort, error)) ProbeResult {
	return probeListenerObservationSource(ctx, func(ctx context.Context, port, pid int) ([]ListeningPort, string, error) {
		rows, err := scan(ctx, port, pid)
		return rows, "", err
	})
}

func probeListenerObservationSource(ctx context.Context, scan func(context.Context, int, int) ([]ListeningPort, string, error)) ProbeResult {
	if err := ctx.Err(); err != nil {
		return ProbeResult{State: ObservationBlind, Code: CodeObservationBlind, Detail: "probe cancelled", Hint: observationBlindHint, Err: err}
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return ProbeResult{
			State: ObservationBindDenied, Code: CodeObservationBindDenied,
			Detail: "cannot bind a local port: " + err.Error(),
			Hint:   observationBindDeniedHint, Err: err,
		}
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	pid := os.Getpid()
	rows, source, err := scan(ctx, port, pid)
	if err != nil {
		return ProbeResult{
			State: ObservationBlind, Code: CodeObservationBlind,
			Detail: fmt.Sprintf("bound 127.0.0.1:%d but the listener scan failed: %v", port, err),
			Hint:   observationBlindHint, Err: err,
		}
	}
	for _, row := range rows {
		if row.Port != port {
			continue
		}
		if row.PID == pid {
			detail := fmt.Sprintf("saw this process's listener on 127.0.0.1:%d (pid %d)", port, pid)
			if source != "" {
				detail += " via " + source
			}
			return ProbeResult{State: ObservationObservable, Detail: detail, Source: source}
		}
		return ProbeResult{
			State: ObservationBlind, Code: CodeObservationBlind,
			Detail: fmt.Sprintf("saw 127.0.0.1:%d but its owner pid is %d, not this process (%d)", port, row.PID, pid),
			Hint:   observationBlindHint,
		}
	}
	return ProbeResult{
		State: ObservationBlind, Code: CodeObservationBlind,
		Detail: fmt.Sprintf("bound 127.0.0.1:%d but the listener table did not show it", port),
		Hint:   observationBlindHint,
	}
}
