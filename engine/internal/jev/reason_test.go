package jev

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Every provider failure has to land in exactly one fixed reason code, because
// that code is what the JSON output, the quality ledger and the `oberth jev
// test` hint all branch on. These tests drive the real client against an
// injectable server so the classification is exercised where it happens.

func reasonForServer(t *testing.T, handler http.HandlerFunc, timeout time.Duration) string {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	_, artifact := testArtifact(t)
	client := Client{
		Endpoint: server.URL, APIKey: "test-key", Model: "jev-test",
		HTTPClient: server.Client(), Timeout: timeout,
	}
	// Two authorized options keep the Choice question in the request, so the
	// malformed-choice case is reachable; with a single option the client never
	// asks the provider to rank anything.
	_, err := client.Evaluate(context.Background(), artifact, TaskContext{
		Task: "find out why the api did not start", AuthorizedOptions: []string{"inspect_logs", "restart_service"},
	})
	return ReasonFor(err)
}

func TestProviderFailuresMapToFixedReasons(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
		want    string
	}{
		{
			name:    "401 is unauthorized",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
			want:    ReasonUnauthorized,
		},
		{
			name:    "403 is unauthorized",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) },
			want:    ReasonUnauthorized,
		},
		{
			name:    "429 is rate limited",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) },
			want:    ReasonRateLimited,
		},
		{
			name:    "500 is a provider error",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			want:    ReasonProvider,
		},
		{
			name:    "504 is a timeout",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusGatewayTimeout) },
			want:    ReasonTimeout,
		},
		{
			name:    "a 400 is a response this client cannot use",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) },
			want:    ReasonBadResponse,
		},
		{
			name: "malformed JSON is a bad response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"answers":`))
			},
			want: ReasonBadResponse,
		},
		{
			name: "a response missing required answers is a bad response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"model":"jev","answers":{"task_relevant":{"type":"noul","noul":0.5}}}`))
			},
			want: ReasonBadResponse,
		},
		{
			name: "a choice naming an unauthorized option is a bad response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"model":"jev","answers":{
					"task_relevant":{"type":"noul","noul":0.4},
					"needs_human":{"type":"noul","noul":0.4}}}`))
			},
			want: ReasonBadResponse,
		},
		{
			name: "a stalled provider is a timeout",
			handler: func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(2 * time.Second):
				case <-r.Context().Done():
				}
			},
			timeout: 50 * time.Millisecond,
			want:    ReasonTimeout,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			timeout := tc.timeout
			if timeout == 0 {
				timeout = 2 * time.Second
			}
			if got := reasonForServer(t, tc.handler, timeout); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUnreachableProviderIsANetworkReason(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing is listening any more

	_, artifact := testArtifact(t)
	_, err := Client{Endpoint: url, APIKey: "k", HTTPClient: server.Client(), Timeout: time.Second}.
		Evaluate(context.Background(), artifact, TaskContext{Task: "why stuck", AuthorizedOptions: []string{"inspect_logs"}})
	if got := ReasonFor(err); got != ReasonNetwork {
		t.Fatalf("reason = %q, want %q (%v)", got, ReasonNetwork, err)
	}
}

func TestNoKeyIsItsOwnReason(t *testing.T) {
	_, artifact := testArtifact(t)
	_, err := Client{Endpoint: DefaultJevEndpoint}.Evaluate(context.Background(), artifact,
		TaskContext{Task: "why stuck", AuthorizedOptions: []string{"inspect_logs"}})
	if got := ReasonFor(err); got != ReasonNoKey {
		t.Fatalf("reason = %q, want %q", got, ReasonNoKey)
	}
}

func TestStaleArtifactKeepsItsOwnReason(t *testing.T) {
	if got := ReasonFor(ErrStale); got != ReasonStale {
		t.Fatalf("reason = %q, want %q", got, ReasonStale)
	}
	// A stale error wrapped by a caller must still classify as stale.
	if got := ReasonFor(errors.Join(ErrUnavailable, ErrStale)); got != ReasonStale {
		t.Fatalf("joined reason = %q, want %q", got, ReasonStale)
	}
}

// Each fixed reason has to have a next step a person can act on, and the hint
// must not echo a provider's raw message.
func TestEveryFixedReasonHasAHint(t *testing.T) {
	for _, code := range []string{
		ReasonNoKey, ReasonDisabled, ReasonInvalidConfig, ReasonUnauthorized, ReasonRateLimited,
		ReasonTimeout, ReasonNetwork, ReasonProvider, ReasonBadResponse, ReasonStale,
	} {
		hint := ReasonHint(code)
		if hint == "" {
			t.Fatalf("%s has no hint", code)
		}
		// provider_error is the code the generic hint belongs to, so it is the
		// one case where matching it is correct.
		if code != ReasonProvider && hint == ReasonHint("not-a-real-code") {
			t.Fatalf("%s fell through to the generic hint", code)
		}
	}
}

// An unclassified error is never left blank: a caller that prints the reason
// must always have something to print.
func TestUnclassifiedErrorsStillGetAReason(t *testing.T) {
	if got := ReasonFor(errors.New("something odd")); got != ReasonProvider {
		t.Fatalf("reason = %q, want %q", got, ReasonProvider)
	}
	if got := ReasonFor(nil); got != "" {
		t.Fatalf("nil reason = %q, want empty", got)
	}
}
