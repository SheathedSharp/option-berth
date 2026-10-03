package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/attention"
)

func probability(v float64) *float64 { return &v }

func TestHILPolicyBoundaries(t *testing.T) {
	base := func() *attention.Assessment {
		return &attention.Assessment{
			NeedsHuman: probability(.1), Confidence: probability(.9), TaskRelevant: probability(.9),
			NextOption: "inspect_logs", Probabilities: map[string]float64{"inspect_logs": .8, "restart_service": .2},
		}
	}
	cases := []struct {
		name   string
		mutate func(*attention.Assessment)
		human  bool
		reason string
	}{
		{"safe inspection", func(*attention.Assessment) {}, false, "confident_read_only_step"},
		{"restart despite low score", func(a *attention.Assessment) { a.NextOption = "restart_service" }, true, "explicit_choice_required"},
		{"start", func(a *attention.Assessment) { a.NextOption = "start_service" }, true, "explicit_choice_required"},
		{"stop", func(a *attention.Assessment) { a.NextOption = "stop_service" }, true, "explicit_choice_required"},
		{"adopt", func(a *attention.Assessment) { a.NextOption = "adopt_manifest" }, true, "explicit_choice_required"},
		{"context", func(a *attention.Assessment) { a.NextOption = "ask_user_for_context" }, true, "explicit_choice_required"},
		{"high probability", func(a *attention.Assessment) { a.NeedsHuman = probability(.70) }, true, "human_probability_high"},
		{"threshold is safe at .30", func(a *attention.Assessment) { a.NeedsHuman = probability(.30) }, false, "confident_read_only_step"},
		{"uncertain probability", func(a *attention.Assessment) { a.NeedsHuman = probability(.31) }, true, "uncertain_assessment"},
		{"low confidence", func(a *attention.Assessment) { a.Confidence = probability(.69) }, true, "uncertain_assessment"},
		{"confidence threshold is safe at .70", func(a *attention.Assessment) { a.Confidence = probability(.70) }, false, "confident_read_only_step"},
		{"close options", func(a *attention.Assessment) {
			a.Probabilities = map[string]float64{"inspect_logs": .55, "restart_service": .45}
		}, true, "close_options"},
		{"option gap threshold is safe at .20", func(a *attention.Assessment) {
			a.Probabilities = map[string]float64{"inspect_logs": .60, "restart_service": .40}
		}, false, "confident_read_only_step"},
		{"missing confidence", func(a *attention.Assessment) { a.Confidence = nil }, true, "incomplete_assessment"},
		{"unknown option", func(a *attention.Assessment) { a.NextOption = "run_shell" }, true, "unknown_option"},
		{"ignore relevant event", func(a *attention.Assessment) {
			a.NextOption = "continue_without_action"
			a.Probabilities = map[string]float64{"continue_without_action": 1}
		}, true, "relevant_event_requires_choice"},
		{"ignore unrelated event", func(a *attention.Assessment) {
			a.TaskRelevant = probability(.1)
			a.NextOption = "continue_without_action"
			a.Probabilities = map[string]float64{"continue_without_action": 1}
		}, false, "confident_read_only_step"},
		{"relevance threshold is safe at .30", func(a *attention.Assessment) {
			a.TaskRelevant = probability(.30)
			a.NextOption = "continue_without_action"
			a.Probabilities = map[string]float64{"continue_without_action": 1}
		}, false, "confident_read_only_step"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := base()
			tc.mutate(a)
			got := decideHIL(a)
			if got.NeedsHuman != tc.human || got.Reason != tc.reason {
				t.Fatalf("decision = %+v", got)
			}
		})
	}
	if !decideHIL(nil).NeedsHuman {
		t.Fatal("no assessment must ask")
	}
	// Regression metrics for every policy branch. These labels describe the
	// declared safety policy, not empirical model quality; production quality
	// still needs a separately labeled task corpus.
	tp, tn, fp, fn := 0, 0, 0, 0
	for _, tc := range cases {
		a := base()
		tc.mutate(a)
		got := decideHIL(a).NeedsHuman
		switch {
		case got && tc.human:
			tp++
		case !got && !tc.human:
			tn++
		case got && !tc.human:
			fp++
		case !got && tc.human:
			fn++
		}
	}
	precision := float64(tp) / float64(tp+fp)
	recall := float64(tp) / float64(tp+fn)
	f1 := 2 * precision * recall / (precision + recall)
	t.Logf("hil_policy_metrics={\"rows\":%d,\"true_positive\":%d,\"true_negative\":%d,\"false_positive\":%d,\"false_negative\":%d,\"precision\":%.3f,\"recall\":%.3f,\"f1\":%.3f}", len(cases), tp, tn, fp, fn, precision, recall, f1)
	if fp != 0 || fn != 0 {
		t.Fatalf("HIL policy regression failed: tp=%d tn=%d fp=%d fn=%d", tp, tn, fp, fn)
	}
}

func runnerFixture(t *testing.T) (runnerStatus, attention.Artifact) {
	t.Helper()
	root := t.TempDir()
	now := time.Now().UTC()
	a, ok := attention.Derive(attention.Facts{WorktreeRoot: root, ObservedAt: now, Services: []attention.Service{
		{Name: "api", LastExit: &attention.Exit{Code: 1, Reason: "start_failed", At: now, RunID: "api-1"}},
		{Name: "worker", LastExit: &attention.Exit{Code: 1, Reason: "crashed", At: now, RunID: "worker-1"}},
	}}, now, time.Minute)
	if !ok {
		t.Fatal("fixture has no anomaly")
	}
	path, err := attention.Write(t.TempDir(), a)
	if err != nil {
		t.Fatal(err)
	}
	s := runnerStatus{AttentionPath: path, AttentionRevision: a.StateRevision}
	s.Scope.Root = root
	return s, a
}

func authorizedFor(a attention.Artifact) []string {
	result := make([]string, 0, len(a.Options))
	for _, option := range a.Options {
		result = append(result, option.ID)
	}
	return result
}

func TestAutoNormalDoesNotCallModel(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	out, err := runAuto(context.Background(), "unused", ".", attention.Client{Endpoint: srv.URL, APIKey: "test", HTTPClient: srv.Client()}, attention.TaskContext{Task: "edit docs"},
		func(context.Context, string, string) (runnerStatus, error) { return runnerStatus{}, nil })
	if err != nil || out.Status != "clear" || calls != 0 {
		t.Fatalf("out=%+v err=%v calls=%d", out, err, calls)
	}
}

func TestAutoAssessesRefreshesCachesAndBindsTask(t *testing.T) {
	s, a := runnerFixture(t)
	calls, reads := 0, 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			State struct {
				Authorized []string          `json:"authorized_options"`
				Events     []attention.Event `json:"events"`
			} `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.State.Events) != 2 {
			t.Error("lost simultaneous anomaly")
		}
		p := map[string]float64{}
		for _, id := range body.State.Authorized {
			p[id] = 0
		}
		p["inspect_logs"] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-test", "answers": map[string]any{
			"task_relevant": map[string]any{"type": "noul", "noul": .9}, "needs_human": map[string]any{"type": "noul", "noul": .1},
			"next_option": map[string]any{"type": "choice", "choice": "inspect_logs", "probabilities": p, "confidence": .9},
		}})
	}))
	defer srv.Close()
	client := attention.Client{Endpoint: srv.URL, APIKey: "test", HTTPClient: srv.Client()}
	read := func(context.Context, string, string) (runnerStatus, error) { reads++; return s, nil }
	task := attention.TaskContext{Task: "fix api", AuthorizedOptions: authorizedFor(a)}
	out, err := runAuto(context.Background(), "unused", ".", client, task, read)
	if err != nil || out.Status != "continue" || out.Cached || !out.JevCalled || calls != 1 || reads != 2 {
		t.Fatalf("out=%+v err=%v calls=%d reads=%d", out, err, calls, reads)
	}
	stored, err := attention.Read(s.AttentionPath)
	if err != nil || stored.Assessment == nil || stored.StateRevision != a.StateRevision || stored.EventID != a.EventID {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if len(out.Request.Events) != 2 || len(out.Request.Options) != len(a.Options) {
		t.Fatalf("request=%+v", out.Request)
	}
	for _, opt := range out.Request.Options {
		if opt.ID == "restart_service" && !opt.RequiresExplicitChoice {
			t.Fatal("restart lost permission boundary")
		}
	}
	out, err = runAuto(context.Background(), "unused", ".", client, task, read)
	if err != nil || !out.Cached || calls != 1 {
		t.Fatalf("cache out=%+v err=%v calls=%d", out, err, calls)
	}
	task.Task = "run integration tests"
	out, err = runAuto(context.Background(), "unused", ".", client, task, read)
	if err != nil || out.Cached || calls != 2 {
		t.Fatalf("task cache out=%+v err=%v calls=%d", out, err, calls)
	}
}

func TestAutoUnavailableKeepsFactsAndRequestsHuman(t *testing.T) {
	for _, mode := range []string{"no_key", "http_error", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			s, artifact := runnerFixture(t)
			before, _ := os.ReadFile(s.AttentionPath)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "no_key" {
					t.Error("no key made HTTP call")
				}
				if mode == "http_error" {
					w.WriteHeader(429)
				} else {
					_, _ = w.Write([]byte(`{"answers":{"next_option":"execute shell"}}`))
				}
			}))
			defer srv.Close()
			client := attention.Client{Endpoint: srv.URL, APIKey: "test", HTTPClient: srv.Client()}
			if mode == "no_key" {
				client.APIKey = ""
			}
			out, err := runAuto(context.Background(), "unused", ".", client, attention.TaskContext{Task: "fix api", AuthorizedOptions: authorizedFor(artifact)}, func(context.Context, string, string) (runnerStatus, error) { return s, nil })
			after, _ := os.ReadFile(s.AttentionPath)
			if err != nil || out.Status != "unavailable" || out.JevCalled != (mode != "no_key") || !out.Decision.NeedsHuman || out.Assessment != nil || out.Request == nil || string(before) != string(after) {
				t.Fatalf("out=%+v err=%v changed=%t", out, err, string(before) != string(after))
			}
		})
	}
}

func TestAutoRefusesStaleAndCrossWorktreeRequests(t *testing.T) {
	for _, mode := range []string{"revision", "root", "expired", "changed_during_call", "recovered_during_call"} {
		t.Run(mode, func(t *testing.T) {
			s, a := runnerFixture(t)
			reads := 0
			if mode == "revision" {
				s.AttentionRevision = "other"
			}
			if mode == "root" {
				s.Scope.Root = "/other/project"
			}
			if mode == "expired" {
				a.FreshUntil = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
				data, _ := json.Marshal(a)
				_ = os.WriteFile(s.AttentionPath, data, 0600)
			}
			out, err := runAuto(context.Background(), "unused", ".", attention.Client{}, attention.TaskContext{Task: "fix", AuthorizedOptions: authorizedFor(a)}, func(context.Context, string, string) (runnerStatus, error) {
				reads++
				copy := s
				if reads == 2 && mode == "changed_during_call" {
					copy.AttentionRevision = "new"
				}
				if reads == 2 && mode == "recovered_during_call" {
					copy.AttentionPath = ""
				}
				return copy, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "recovered_during_call" {
				if out.Status != "clear" {
					t.Fatalf("out=%+v", out)
				}
				return
			}
			if out.Status != "stale" || out.Request != nil || out.Assessment != nil {
				t.Fatalf("out=%+v", out)
			}
		})
	}
}

func TestSubscribeDeduplicatesSameRevisionAndStops(t *testing.T) {
	s, artifact := runnerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	reads := 0
	err := runSubscribe(ctx, "unused", ".", time.Millisecond, attention.Client{}, attention.TaskContext{
		Task: "fix api", AuthorizedOptions: authorizedFor(artifact),
	}, func(context.Context, string, string) (runnerStatus, error) {
		reads++
		if reads >= 4 {
			cancel()
		}
		return s, nil
	}, func(value any) error { return writeJSON(&output, value) })
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	decoder := json.NewDecoder(&output)
	for {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 3 || events[0]["status"] != "subscribed" || events[1]["status"] != "unavailable" || events[2]["status"] != "stopped" {
		t.Fatalf("subscription events = %+v (reads=%d)", events, reads)
	}
}

func TestSubscribeDiscoversAnomalyAfterClear(t *testing.T) {
	s, artifact := runnerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	reads := 0
	err := runSubscribe(ctx, "unused", ".", time.Millisecond, attention.Client{}, attention.TaskContext{
		Task: "fix api", AuthorizedOptions: authorizedFor(artifact),
	}, func(context.Context, string, string) (runnerStatus, error) {
		reads++
		if reads == 1 {
			return runnerStatus{}, nil
		}
		if reads >= 4 {
			cancel()
		}
		return s, nil
	}, func(value any) error { return writeJSON(&output, value) })
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	decoder := json.NewDecoder(&output)
	for {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			break
		}
		events = append(events, event)
	}
	if len(events) != 4 || events[0]["status"] != "subscribed" || events[1]["status"] != "clear" || events[2]["status"] != "unavailable" || events[3]["status"] != "stopped" {
		t.Fatalf("subscription transition events = %+v (reads=%d)", events, reads)
	}
}

func TestSubscribeStatsExplainQuietAndUnavailablePolls(t *testing.T) {
	s, artifact := runnerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	stats, err := runSubscribeWithStats(ctx, "unused", ".", time.Millisecond, attention.Client{}, attention.TaskContext{
		Task: "fix api", AuthorizedOptions: authorizedFor(artifact),
	}, func(context.Context, string, string) (runnerStatus, error) {
		reads++
		if reads == 1 {
			return runnerStatus{}, nil
		}
		if reads >= 3 {
			cancel()
		}
		return s, nil
	}, "session-test", nil, func(any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if stats.Polls != 2 || stats.NoEventPolls != 1 || stats.EventsSeen != 1 || stats.JevCalls != 0 || stats.Unavailable != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestAutoRequiresExplicitAuthorizedOptions(t *testing.T) {
	s, _ := runnerFixture(t)
	_, err := runAuto(context.Background(), "unused", ".", attention.Client{}, attention.TaskContext{Task: "fix api"}, func(context.Context, string, string) (runnerStatus, error) { return s, nil })
	if err == nil || !strings.Contains(err.Error(), "explicit authorized_options") {
		t.Fatalf("err = %v, want explicit authorization error", err)
	}
}

func TestAutoSendsOnlyExplicitAuthorizedCandidates(t *testing.T) {
	s, artifact := runnerFixture(t)
	var authorized []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State struct {
				Authorized []string `json:"authorized_options"`
			} `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		authorized = append(authorized, body.State.Authorized...)
		_, _ = w.Write([]byte(`{"model":"jev-test","answers":{"task_relevant":{"type":"noul","noul":0.4},"needs_human":{"type":"noul","noul":0.2}}}`))
	}))
	defer srv.Close()
	out, err := runAuto(context.Background(), "unused", ".", attention.Client{Endpoint: srv.URL, APIKey: "test", HTTPClient: srv.Client()}, attention.TaskContext{
		Task: "inspect api", AuthorizedOptions: []string{"inspect_logs"},
	}, func(context.Context, string, string) (runnerStatus, error) { return s, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(authorized) != 1 || authorized[0] != "inspect_logs" {
		t.Fatalf("provider authorization = %v", authorized)
	}
	if out.Request == nil || len(out.Request.Options) != 1 || out.Request.Options[0].ID != "inspect_logs" {
		t.Fatalf("request options = %+v (artifact=%+v)", out.Request, artifact.Options)
	}
	if out.ContextID == "" || out.Assessment == nil || out.ContextID != out.Assessment.ContextID {
		t.Fatalf("context correlation = output=%q assessment=%+v", out.ContextID, out.Assessment)
	}
}

func TestAutoIntersectsBroadKnownAuthorizationWithEventCandidates(t *testing.T) {
	s, artifact := runnerFixture(t)
	wanted := artifact.Options[0].ID
	absent := ""
	for _, policy := range attention.OptionCatalog() {
		found := false
		for _, option := range artifact.Options {
			if policy.ID == option.ID {
				found = true
				break
			}
		}
		if !found {
			absent = policy.ID
			break
		}
	}
	if absent == "" {
		t.Fatal("fixture unexpectedly offers the complete fixed option catalog")
	}
	var authorized []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State struct {
				Authorized []string `json:"authorized_options"`
			} `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		authorized = append(authorized, body.State.Authorized...)
		_, _ = w.Write([]byte(`{"model":"jev-test","answers":{"task_relevant":{"type":"noul","noul":0.4},"needs_human":{"type":"noul","noul":0.2}}}`))
	}))
	defer srv.Close()
	out, err := runAuto(context.Background(), "unused", ".", attention.Client{Endpoint: srv.URL, APIKey: "test", HTTPClient: srv.Client()}, attention.TaskContext{
		Task: "inspect api", AuthorizedOptions: []string{absent, wanted},
	}, func(context.Context, string, string) (runnerStatus, error) { return s, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(authorized) != 1 || authorized[0] != wanted {
		t.Fatalf("provider authorization = %v, absent=%q wanted=%q", authorized, absent, wanted)
	}
	if out.Request == nil || len(out.Request.Options) != 1 || out.Request.Options[0].ID != wanted {
		t.Fatalf("request options = %+v", out.Request)
	}
}

func TestRunnerExecutesOnlyStatusAndRejectsExtraJSON(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "oberth")
	trace := filepath.Join(dir, "trace")
	t.Setenv("RUNNER_TRACE", trace)
	for _, body := range []string{`{"scope":{"root":"/fixture"}}`, `{"scope":{"root":"/fixture"}} {}`} {
		// Constant fixture text; no user input is interpolated into shell.
		script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > \"$RUNNER_TRACE\"\ncat <<'JSON'\n" + body + "\nJSON\n"
		if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		_, err := readRunnerStatus(context.Background(), binary, dir)
		if strings.HasSuffix(body, " {}") && err == nil {
			t.Fatal("extra JSON accepted")
		}
		if !strings.HasSuffix(body, " {}") && err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(trace)
		if string(data) != dir+"\nstatus\n--json\n--no-mark\n" {
			t.Fatalf("executed %q", data)
		}
	}
}
