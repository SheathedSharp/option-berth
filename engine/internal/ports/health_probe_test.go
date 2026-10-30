package ports

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"
)

func TestProbeTCPReportsAReachableListener(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	got := ProbeTCPContext(context.Background(), "127.0.0.1", port, time.Second)
	if got.Status != "healthy" {
		t.Fatalf("status = %q, want healthy", got.Status)
	}
}

func TestProbeTCPReportsRefused(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	got := ProbeTCPContext(context.Background(), "127.0.0.1", port, time.Second)
	if got.Status != "refused" {
		t.Fatalf("status = %q, want refused (%+v)", got.Status, got)
	}
}

func TestProbeCommandUsesExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	ok := ProbeCommandContext(context.Background(), []string{"sh", "-c", "exit 0"}, "", nil, time.Second)
	if ok.Status != "healthy" {
		t.Fatalf("ok status = %q, want healthy", ok.Status)
	}
	bad := ProbeCommandContext(context.Background(), []string{"sh", "-c", "exit 7"}, "", nil, time.Second)
	if bad.Status != "command-failed" || bad.StatusCode != 7 {
		t.Fatalf("bad = %+v, want command-failed with code 7", bad)
	}
	missing := ProbeCommandContext(context.Background(), []string{"/definitely/not/a/command"}, "", nil, time.Second)
	if missing.Status != "command-error" {
		t.Fatalf("missing = %+v, want command-error", missing)
	}
}

func TestProbeCommandTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	got := ProbeCommandContext(context.Background(), []string{"sh", "-c", "sleep 5"}, "", nil, 30*time.Millisecond)
	if got.Status != "timeout" {
		t.Fatalf("status = %q, want timeout", got.Status)
	}
}

func TestProbeCommandCancellationIsNotAVerdict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := ProbeCommandContext(ctx, []string{"sh", "-c", "sleep 5"}, "", nil, time.Second)
	if got.Status != "" {
		t.Fatalf("status = %q, want no observation on cancellation", got.Status)
	}
}
