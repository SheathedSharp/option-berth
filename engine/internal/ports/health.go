package ports

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// HealthResult is what one probe observed.
type HealthResult struct {
	Status     string
	StatusCode int
	Latency    time.Duration

	// ObservedAt is when the probe ran, not when a snapshot was assembled.
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
	target := HostForBind(host)
	if strings.Contains(target, ":") {
		target = "[" + target + "]"
	}
	client := probeClient(timeout)

	start := time.Now()
	observedAt := start.UTC()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s:%d%s", target, port, path), nil)
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

// probeClient is the client one probe uses: no redirects are followed, and the
// whole request is bounded by timeout.
func probeClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// isConnectionRefused reports whether the error chain says the dial was
// refused. The standard library wraps the syscall error in several layers, so
// the inner error is unwrapped first and the text is the final fallback.
func isConnectionRefused(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return isConnectionRefused(urlErr.Err)
	}
	return err != nil && strings.Contains(err.Error(), "connection refused")
}

// MaxProbes is how many health probes may run at once, the same ceiling the
// scanner's configured-health probe uses.
const MaxProbes = 10

// EnrichHealth probes every port concurrently, at most MaxProbes at a time,
// and fills each row's Health fields.
//
// budget bounds the whole round, not one probe. On a machine with forty
// listeners, ten of them sockets that accept and never answer, the old form
// cost four waves of `timeout` — seconds during which a `ports.kill` or a
// manifest write waited behind the scan. A probe that would start after the
// budget is spent is skipped (its row keeps the previous tick's health, see
// carryHealth), and one starting near the end has its timeout clamped to what
// remains. Zero or negative budget means no ceiling.
func EnrichHealth(pp []ListeningPort, timeout, budget time.Duration) {
	EnrichHealthContext(context.Background(), pp, timeout, budget)
}

// EnrichHealthContext stops admitting probes on cancellation and joins all
// admitted probes before returning. The caller owns pp and must discard a
// cancelled round; probes completed before cancellation may already be in pp.
func EnrichHealthContext(ctx context.Context, pp []ListeningPort, timeout, budget time.Duration) {
	var cutoff time.Time
	if budget > 0 {
		cutoff = time.Now().Add(budget)
	}

	var group sync.WaitGroup
	slots := make(chan struct{}, MaxProbes)

admit:
	for i := range pp {
		if ctx.Err() != nil {
			break
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			break admit
		}
		if _, ok := ProbeBudget(timeout, cutoff); !ok || ctx.Err() != nil {
			<-slots
			break
		}
		group.Add(1)
		go func(index int) {
			defer group.Done()
			defer func() { <-slots }()
			probeTimeout, ok := ProbeBudget(timeout, cutoff)
			if !ok {
				return
			}
			result := ProbeHealthContext(ctx, pp[index].BindAddress, pp[index].Port, "/", probeTimeout)
			if ctx.Err() != nil {
				return
			}
			pp[index].HealthStatus = result.Status
			pp[index].HealthCode = result.StatusCode
			pp[index].HealthLatency = result.Latency
			pp[index].HealthObservedAt = result.ObservedAt.Format(time.RFC3339Nano)
		}(i)
	}
	group.Wait()
}

// ProbeBudget is the timeout one probe may use before cutoff, and whether it is
// worth starting. A zero cutoff means no budget: the probe gets its full
// timeout.
func ProbeBudget(timeout time.Duration, cutoff time.Time) (time.Duration, bool) {
	if cutoff.IsZero() {
		return timeout, true
	}
	remaining := time.Until(cutoff)
	if remaining <= 0 {
		return 0, false
	}
	if remaining < timeout {
		return remaining, true
	}
	return timeout, true
}
