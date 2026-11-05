package ports

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestIsConnectionRefused(t *testing.T) {
	cases := []struct {
		label string
		err   error
		want  bool
	}{
		{
			label: "refused",
			err:   errors.New("dial tcp: connection refused"),
			want:  true,
		},
		{
			label: "wrapped in url.Error",
			err:   &url.Error{Op: "Get", URL: "http://localhost:9999", Err: errors.New("connection refused")},
			want:  true,
		},
		{
			label: "timeout",
			err:   errors.New("i/o timeout"),
			want:  false,
		},
		{
			label: "unrelated",
			err:   errors.New("something else went wrong"),
			want:  false,
		},
		{
			label: "no error",
			err:   nil,
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := isConnectionRefused(tc.err); got != tc.want {
				t.Errorf("isConnectionRefused(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Ten probes run at once, so forty listeners that accept and never answer used
// to cost four waves of the per-probe timeout inside the scan lock. The round
// is bounded; unreached ports keep the previous tick's health.
func TestEnrichHealthRoundIsBudgeted(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	defer listener.Close()
	// Accept and never answer, so a probe waits out its timeout.
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	rows := make([]ListeningPort, 32)
	for i := range rows {
		rows[i] = ListeningPort{Port: port, BindAddress: "127.0.0.1"}
	}

	start := time.Now()
	EnrichHealth(rows, time.Second, 250*time.Millisecond)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a round of 32 probes took %s, want it bounded by the budget", took)
	}
}

func TestProbeBudget(t *testing.T) {
	if limit, ok := ProbeBudget(3*time.Second, time.Time{}); !ok || limit != 3*time.Second {
		t.Errorf("no cutoff: got %s/%v, want the full timeout", limit, ok)
	}
	if _, ok := ProbeBudget(time.Second, time.Now().Add(-time.Millisecond)); ok {
		t.Error("a spent budget started another probe")
	}
	limit, ok := ProbeBudget(time.Second, time.Now().Add(40*time.Millisecond))
	if !ok || limit > 40*time.Millisecond {
		t.Errorf("near the cutoff: got %s/%v, want the timeout clamped", limit, ok)
	}
}
