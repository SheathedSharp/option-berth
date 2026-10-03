package attention

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testArtifact(t *testing.T) (string, Artifact) {
	t.Helper()
	home := t.TempDir()
	root := "/Users/alice/work/my-repo"
	now := time.Now().UTC()
	a, ok := Derive(Facts{
		StateRevision: "184",
		ObservedAt:    now,
		WorktreeRoot:  root,
		Branch:        "feature/attention",
		Services: []Service{{Name: "api", LastExit: &Exit{
			Code: 1, Reason: "start_failed", At: now,
		}}},
	}, now, time.Minute)
	if !ok {
		t.Fatal("expected anomaly artifact")
	}
	path, err := Write(home, a)
	if err != nil {
		t.Fatal(err)
	}
	return path, a
}

func TestJevEvaluateRedactsStateAndPersistsOnlyValidatedAssessment(t *testing.T) {
	path, artifact := testArtifact(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		var request jevRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(request)
		body := string(encoded)
		for _, forbidden := range []string{"/Users/alice", "/tmp/private", "password=secret", "latest api log line"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("request leaked %q: %s", forbidden, body)
			}
		}
		if request.State.WorktreeID == "/Users/alice/work/my-repo" || request.State.WorktreeID == "" {
			t.Errorf("worktree was not hashed: %+v", request.State)
		}
		if request.State.Branch != "feature/attention" {
			t.Errorf("branch = %q, want bounded branch label", request.State.Branch)
		}
		if len(request.State.EvidenceRefs) != len(artifact.Evidence) || strings.Contains(strings.Join(request.State.EvidenceRefs, "|"), "<redacted>") {
			t.Errorf("safe evidence refs = %v, want bounded generated refs", request.State.EvidenceRefs)
		}
		if request.Questions["needs_human"].Type != "noul" || request.Questions["next_option"].Type != "choice" {
			t.Errorf("typed questions = %+v", request.Questions)
		}
		if len(request.State.Events) != len(artifact.Events) || request.State.Event.Kind != string(artifact.Events[0].Kind) {
			t.Errorf("event summaries = %+v, want all artifact events with first-event compatibility", request.State)
		}
		if _, ok := request.Questions["next_option"].Criteria.(map[string]any)["restart_service"]; !ok {
			t.Errorf("choice criteria = %+v", request.Questions["next_option"].Criteria)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"task_relevant":{"type":"noul","noul":0.94},"needs_human":{"type":"noul","noul":0.88},"next_option":{"type":"choice","choice":"restart_service","probabilities":{"inspect_logs":0.12,"restart_service":0.78,"continue_without_action":0.10},"confidence":0.76}}}`))
	}))
	defer server.Close()
	result, err := (Client{
		Endpoint:   server.URL,
		APIKey:     "test-key",
		HTTPClient: server.Client(),
	}).Evaluate(context.Background(), artifact, TaskContext{
		Task:              "Fix API startup; token=secret; see /Users/alice/work/my-repo/latest api log line at /tmp/private",
		AuthorizedOptions: []string{"inspect_logs", "restart_service", "continue_without_action"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Assessment.NextOption != "restart_service" || result.Assessment.TaskRelevant == nil || *result.Assessment.TaskRelevant != 0.94 {
		t.Fatalf("result = %+v", result)
	}
	if err := Persist(path, artifact, result); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventID != artifact.EventID || got.Assessment == nil || got.Assessment.Model != "jev-1.13.0" {
		t.Fatalf("persisted artifact = %+v", got)
	}
	if got.Assessment.ContextID == "" {
		t.Fatal("persisted assessment has no task context identity")
	}
}

func TestJevNoKeyLeavesArtifactUntouched(t *testing.T) {
	path, artifact := testArtifact(t)
	_, err := (Client{}).Evaluate(context.Background(), artifact, TaskContext{Task: "fix", AuthorizedOptions: []string{"inspect_logs"}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Assessment != nil {
		t.Fatalf("no-key call mutated artifact: %+v", got.Assessment)
	}
}

func TestJevRejectsInvalidProbabilitiesAndUnauthorizedChoice(t *testing.T) {
	path, artifact := testArtifact(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-latest","answers":{"task_relevant":{"type":"noul","noul":0.5},"needs_human":{"type":"noul","noul":0.5},"next_option":{"type":"choice","choice":"run_shell","probabilities":{"inspect_logs":1.2,"restart_service":-0.2},"confidence":1.2}}}`))
	}))
	defer server.Close()
	_, err := (Client{Endpoint: server.URL, APIKey: "k", HTTPClient: server.Client()}).Evaluate(context.Background(), artifact, TaskContext{Task: "fix", AuthorizedOptions: []string{"inspect_logs", "restart_service"}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable validation error", err)
	}
	got, readErr := Read(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got.Assessment != nil {
		t.Fatal("invalid response mutated artifact")
	}
}

func TestJevRejectsStaleArtifactBeforeRemoteCall(t *testing.T) {
	now := time.Now().UTC().Add(-2 * time.Minute)
	a, ok := Derive(Facts{StateRevision: "1", ObservedAt: now, WorktreeRoot: "/tmp/repo", Services: []Service{{Name: "api", LastExit: &Exit{Reason: "start_failed", At: now}}}}, now, time.Second)
	if !ok {
		t.Fatal("expected anomaly artifact")
	}
	_, err := (Client{APIKey: "key"}).Evaluate(context.Background(), a, TaskContext{Task: "fix", AuthorizedOptions: []string{"inspect_logs"}})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

func TestFixedOptionCatalogIsKnownAndDefensiveCopy(t *testing.T) {
	catalog := OptionCatalog()
	if len(catalog) < 9 {
		t.Fatalf("catalog = %+v, want the complete fixed option vocabulary", catalog)
	}
	for _, id := range []string{"inspect_manifest", "start_service", "stop_service", "adopt_manifest"} {
		policy, ok := OptionPolicyFor(id)
		if !ok || policy.ID != id {
			t.Fatalf("option %q missing from catalog", id)
		}
	}
	catalog[0].ID = "arbitrary_shell"
	if KnownOptionID("arbitrary_shell") {
		t.Fatal("catalog mutation changed the fixed vocabulary")
	}
}

func TestFilterAuthorizedOptionsIntersectsBroadFixedAllowList(t *testing.T) {
	options := []Option{{ID: "inspect_logs"}, {ID: "restart_service"}}
	got, err := FilterAuthorizedOptions(options, []string{
		"continue_without_action", "restart_service", "inspect_logs", "inspect_logs",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "inspect_logs,restart_service" {
		t.Fatalf("intersection = %v", got)
	}
	if _, err := FilterAuthorizedOptions(options, []string{"arbitrary_shell", "inspect_logs"}); err == nil || !strings.Contains(err.Error(), "unknown option") {
		t.Fatalf("unknown option err = %v", err)
	}
	if _, err := FilterAuthorizedOptions(options, []string{"inspect_manifest"}); err == nil || !strings.Contains(err.Error(), "offered by this event") {
		t.Fatalf("empty intersection err = %v", err)
	}
}

func TestOptionDescriptionsNeverBecomeShellCommands(t *testing.T) {
	for _, policy := range OptionCatalog() {
		if policy.Description == "" || strings.ContainsAny(policy.Description, "\n\r") {
			t.Fatalf("unsafe description for %q: %q", policy.ID, policy.Description)
		}
		if strings.Contains(policy.Description, "$") || strings.Contains(policy.Description, "&&") {
			t.Fatalf("description for %q contains shell syntax: %q", policy.ID, policy.Description)
		}
	}
}
