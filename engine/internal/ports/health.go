package ports

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// HealthResult holds the outcome of probing a single port.
type HealthResult struct {
	Status     string
	StatusCode int
	Latency    time.Duration
	ObservedAt time.Time
}

// ProbeHealth performs an HTTP GET to host:port/path and classifies the result.
// If path is empty, "/" is used. If host is empty or a wildcard, "localhost" is used.
func ProbeHealth(host string, port int, path string, timeout time.Duration) HealthResult {
	return ProbeHealthContext(context.Background(), host, port, path, timeout)
}

// ProbeHealthContext ties the request to its owner. Owner cancellation is not
// service-health evidence: it returns no observation. A probe's own timeout
// still produces the existing timeout verdict.
func ProbeHealthContext(ctx context.Context, host string, port int, path string, timeout time.Duration) HealthResult {
	if ctx.Err() != nil {
		return HealthResult{}
	}
	if path == "" {
		path = "/"
	}
	host = HostForBind(host)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	start := time.Now()
	observedAt := start.UTC()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s:%d%s", host, port, path), nil)
	if err != nil {
		return HealthResult{Status: "non-http", ObservedAt: observedAt}
	}
	resp, err := client.Do(req)
	latency := time.Since(start)
	if resp != nil {
		defer resp.Body.Close()
	}
	if ctx.Err() != nil {
		return HealthResult{}
	}

	if err != nil {
		if urlErr, ok := err.(*url.Error); ok && urlErr.Timeout() {
			return HealthResult{Status: "timeout", Latency: latency, ObservedAt: observedAt}
		}
		// Connection refused or other dial errors
		if isConnectionRefused(err) {
			return HealthResult{Status: "refused", Latency: latency, ObservedAt: observedAt}
		}
		// Anything else (e.g. non-HTTP server sending garbage)
		return HealthResult{Status: "non-http", Latency: latency, ObservedAt: observedAt}
	}
	code := resp.StatusCode
	if code >= 200 && code < 400 {
		return HealthResult{Status: "healthy", StatusCode: code, Latency: latency, ObservedAt: observedAt}
	}
	return HealthResult{Status: "unhealthy", StatusCode: code, Latency: latency, ObservedAt: observedAt}
}

// isConnectionRefused checks whether the error chain contains a
// "connection refused" indication.
func isConnectionRefused(err error) bool {
	for err != nil {
		if e, ok := err.(*url.Error); ok {
			err = e.Err
			continue
		}
		// Check the string as a fallback — the stdlib wraps the
		// syscall error in various layers.
		if strings.Contains(fmt.Sprintf("%v", err), "connection refused") ||
			strings.Contains(err.Error(), "connection refused") {
			return true
		}
		break
	}
	return false
}

// MaxProbes bounds how many health probes run at once. It is the same ceiling
// the configured-health probe in the scanner uses.
const MaxProbes = 10

// EnrichHealth probes every port concurrently (max MaxProbes at a time) and
// populates the Health* fields on each ListeningPort.
//
// budget is the ceiling on the whole round, not on one probe: a machine with
// forty listeners, ten of them sockets that accept and never answer, used to
// cost four waves of `timeout` each — seconds of a scan that a `ports.kill`
// or a `oberth.yaml` write was queued behind. A probe that would start after
// the budget is spent is skipped (its port keeps whatever health the previous
// tick found, see carryHealth), and one that starts near the end has its own
// timeout clamped to what is left, so the round costs at most budget.
// A zero or negative budget means no ceiling.
func EnrichHealth(pp []ListeningPort, timeout, budget time.Duration) {
	EnrichHealthContext(context.Background(), pp, timeout, budget)
}

// EnrichHealthContext stops admitting probes on cancellation and joins all
// admitted probes before returning. The caller owns pp and must discard a
// cancelled round; probes completed before cancellation may already be in pp.
func EnrichHealthContext(ctx context.Context, pp []ListeningPort, timeout, budget time.Duration) {
	var deadline time.Time
	if budget > 0 {
		deadline = time.Now().Add(budget)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxProbes)

admit:
	for i := range pp {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break admit
		}
		if _, ok := ProbeBudget(timeout, deadline); !ok || ctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			t, ok := ProbeBudget(timeout, deadline)
			if !ok {
				return
			}
			result := ProbeHealthContext(ctx, pp[idx].BindAddress, pp[idx].Port, "/", t)
			if ctx.Err() != nil {
				return
			}
			pp[idx].HealthStatus = result.Status
			pp[idx].HealthCode = result.StatusCode
			pp[idx].HealthLatency = result.Latency
			pp[idx].HealthObservedAt = result.ObservedAt.Format(time.RFC3339Nano)
		}(i)
	}
	wg.Wait()
}

// ProbeBudget is the timeout one probe may use before deadline, and whether it
// is worth starting at all. A zero deadline means no budget: the probe gets its
// full timeout.
func ProbeBudget(timeout time.Duration, deadline time.Time) (time.Duration, bool) {
	if deadline.IsZero() {
		return timeout, true
	}
	left := time.Until(deadline)
	if left <= 0 {
		return 0, false
	}
	if left < timeout {
		return left, true
	}
	return timeout, true
}
