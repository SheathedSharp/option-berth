package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// protocolArtifact is one fresh event with three offered options, so a test
// exercises the choice question without depending on the command package.
func protocolArtifact(t *testing.T) attn.Artifact {
	t.Helper()
	now := time.Now().UTC()
	a, ok := attn.Derive(attn.Facts{
		StateRevision: "rev-protocol", ObservedAt: now, WorktreeRoot: "/Users/alice/work/repo",
		Services: []attn.Service{{Name: "api", LastExit: &attn.Exit{Code: 1, Reason: "start_failed", At: now}}},
	}, now, time.Minute)
	if !ok {
		t.Fatal("expected an anomaly artifact")
	}
	return a
}

func protocolTask() TaskContext {
	return TaskContext{
		Task:              "Fix the API startup; token=supersecret at /Users/alice/config.env",
		AuthorizedOptions: []string{"inspect_logs", "restart_service", "continue_without_action"},
	}
}

// decisionJSON is the exact shape every prompt protocol is asked for.
const decisionJSON = `{"task_relevant":0.91,"needs_human":0.12,"urgency":0.8,"next_option":"restart_service","confidence":0.77,"probabilities":{"inspect_logs":0.1,"restart_service":0.7,"continue_without_action":0.2},"summary":"The API binary is stale after a code change."}`

func TestChatProtocolSendsBearerJSONModeAndValidatesTheDecision(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer chat-key" {
			t.Errorf("authorization = %q", got)
		}
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ResponseFormat == nil || body.ResponseFormat.Type != "json_object" {
			t.Errorf("response_format = %+v, want json_object", body.ResponseFormat)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" {
			t.Fatalf("messages = %+v", body.Messages)
		}
		prompt := body.Messages[0].Content
		for _, want := range []string{"task_relevant", "next_option", "restart_service", "summary"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("schema prompt does not mention %q", want)
			}
		}
		if strings.Contains(body.Messages[1].Content, "/Users/alice") {
			t.Errorf("state leaked a local path: %s", body.Messages[1].Content)
		}
		_, _ = w.Write([]byte(`{"model":"gpt-test","choices":[{"message":{"role":"assistant","content":` + jsonString(decisionJSON) + `}}]}`))
	}))
	defer server.Close()

	result, err := (Client{
		Endpoint: server.URL, Model: "gpt-test", APIKey: "chat-key",
		Protocol: ProtocolOpenAIChat, HTTPClient: server.Client(),
	}).Evaluate(context.Background(), protocolArtifact(t), protocolTask())
	if err != nil {
		t.Fatal(err)
	}
	if result.Assessment.NextOption != "restart_service" || result.Assessment.Urgency == nil || *result.Assessment.Urgency != 0.8 {
		t.Fatalf("assessment = %+v", result.Assessment)
	}
	if result.Assessment.Summary != "The API binary is stale after a code change." {
		t.Fatalf("summary = %q", result.Assessment.Summary)
	}
}

func TestAnthropicProtocolSendsAPIKeyVersionAndMaxTokens(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "anthropic-key" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != anthropicVersion {
			t.Errorf("anthropic-version = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("anthropic request carried a bearer header: %q", got)
		}
		var body anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.MaxTokens != decisionMaxTokens || body.System == "" || len(body.Messages) != 1 {
			t.Fatalf("request = %+v", body)
		}
		_, _ = w.Write([]byte(`{"model":"claude-test","content":[{"type":"text","text":` + jsonString(decisionJSON) + `}]}`))
	}))
	defer server.Close()

	result, err := (Client{
		Endpoint: server.URL, Model: "claude-test", APIKey: "anthropic-key",
		Protocol: ProtocolAnthropic, HTTPClient: server.Client(),
	}).Evaluate(context.Background(), protocolArtifact(t), protocolTask())
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "claude-test" || result.Assessment.NextOption != "restart_service" {
		t.Fatalf("result = %+v", result)
	}
}

// A decisions provider that answers only the three required questions must keep
// working: urgency is optional, and an older or simpler mock must not be forced
// to learn a new answer type.
func TestDecisionsProtocolAcceptsMockWithoutUrgency(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request apiRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Questions["urgency"].Type != "score" {
			t.Errorf("urgency question = %+v, want a score question", request.Questions["urgency"])
		}
		_, _ = w.Write([]byte(`{"model":"jev-test","answers":{"task_relevant":{"type":"noul","noul":0.94},"needs_human":{"type":"noul","noul":0.2},"next_option":{"type":"choice","choice":"restart_service","probabilities":{"inspect_logs":0.1,"restart_service":0.8,"continue_without_action":0.1},"confidence":0.8}}}`))
	}))
	defer server.Close()

	result, err := (Client{
		Endpoint: server.URL, Model: "jev-test", APIKey: "k",
		Protocol: ProtocolDecisions, HTTPClient: server.Client(),
	}).Evaluate(context.Background(), protocolArtifact(t), protocolTask())
	if err != nil {
		t.Fatal(err)
	}
	if result.Assessment.Urgency != nil || result.Assessment.NextOption != "restart_service" {
		t.Fatalf("assessment = %+v", result.Assessment)
	}
}

func TestDecisionsProtocolParsesUrgencyWhenAnswered(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-test","answers":{"task_relevant":{"type":"noul","noul":0.9},"needs_human":{"type":"noul","noul":0.2},"urgency":{"type":"score","score":0.66},"next_option":{"type":"choice","choice":"restart_service","probabilities":{"inspect_logs":0.1,"restart_service":0.8,"continue_without_action":0.1},"confidence":0.8}}}`))
	}))
	defer server.Close()

	result, err := (Client{Endpoint: server.URL, Model: "jev-test", APIKey: "k", HTTPClient: server.Client()}).
		Evaluate(context.Background(), protocolArtifact(t), protocolTask())
	if err != nil {
		t.Fatal(err)
	}
	if result.Assessment.Urgency == nil || *result.Assessment.Urgency != 0.66 {
		t.Fatalf("urgency = %v", result.Assessment.Urgency)
	}
}

func TestEachProtocolRejectsUnauthorizedOptionAndOutOfRangeProbability(t *testing.T) {
	answers := []string{
		// unauthorized option
		`{"task_relevant":0.9,"needs_human":0.2,"next_option":"run_shell","confidence":0.9,"probabilities":{"inspect_logs":0.5,"restart_service":0.5,"continue_without_action":0.0},"summary":"x"}`,
		// probability outside [0,1]
		`{"task_relevant":0.9,"needs_human":0.2,"next_option":"restart_service","confidence":0.9,"probabilities":{"inspect_logs":1.4,"restart_service":-0.4,"continue_without_action":0.0},"summary":"x"}`,
		// probabilities that do not sum to one
		`{"task_relevant":0.9,"needs_human":0.2,"next_option":"restart_service","confidence":0.9,"probabilities":{"inspect_logs":0.5,"restart_service":0.5,"continue_without_action":0.5},"summary":"x"}`,
		// malformed JSON
		`not json at all`,
	}
	for _, protocol := range []Protocol{ProtocolOpenAIChat, ProtocolAnthropic} {
		for i, answer := range answers {
			t.Run(string(protocol)+"/"+string(rune('a'+i)), func(t *testing.T) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if protocol == ProtocolAnthropic {
						_, _ = w.Write([]byte(`{"model":"m","content":[{"type":"text","text":` + jsonString(answer) + `}]}`))
						return
					}
					_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":` + jsonString(answer) + `}}]}`))
				}))
				defer server.Close()
				_, err := (Client{
					Endpoint: server.URL, Model: "m", APIKey: "k", Protocol: protocol, HTTPClient: server.Client(),
				}).Evaluate(context.Background(), protocolArtifact(t), protocolTask())
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("err = %v, want ErrUnavailable", err)
				}
				if reason := ReasonFor(err); reason != ReasonBadResponse {
					t.Fatalf("reason = %q, want %q", reason, ReasonBadResponse)
				}
			})
		}
	}
}

func TestChatProtocolRejectsProseWrappedDecision(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Here is the decision:\n` + strings.ReplaceAll(decisionJSON, `"`, `\"`) + `\nThanks!"}}]}`))
	}))
	defer server.Close()
	_, err := (Client{Endpoint: server.URL, Model: "m", APIKey: "k", Protocol: ProtocolOpenAIChat, HTTPClient: server.Client()}).
		Evaluate(context.Background(), protocolArtifact(t), protocolTask())
	if !errors.Is(err, ErrUnavailable) || ReasonFor(err) != ReasonBadResponse {
		t.Fatalf("err = %v, want a bad-response rejection", err)
	}
}

// jsonString renders s as a JSON string literal.
func jsonString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// ------------------------------------------------------------ world triage ---

func triageWorld() runtimecore.World {
	return runtimecore.World{
		Schema:   runtimecore.WorldSchema,
		Revision: "rev-world-1",
		Worktree: runtimecore.WorldIdentity{
			Group: "repo", Root: "/Users/alice/work/secret-repo", Branch: "feat/attention",
		},
		Coverage: runtimecore.CoverageObservable,
		Health:   runtimecore.WorldBroken,
		Services: []runtimecore.ServiceRuntime{
			{Name: "api", State: runtimecore.ServiceCrashed, PortHolder: &state.PortHolder{
				PID: 999, Kind: state.PortHolderListener, Command: "leaky-command --token supersecret",
			}},
			{Name: "web", State: runtimecore.ServiceReady, Drifts: []runtimecore.Drift{
				{Kind: runtimecore.DriftCodeStale, Severity: runtimecore.SeverityWarning, Summary: "code changed"},
			}},
		},
		Diagnosis: runtimecore.CausalDiagnosis{
			Healthy: false,
			RootCauses: []runtimecore.RootCause{{
				ID: "cause-1", Service: "api", Kind: runtimecore.DriftServiceCrashed,
				Severity: runtimecore.SeverityCritical, Summary: "crashed with exit code 1",
				Impacted:   []string{"web"},
				FirstError: "Error: ECONNREFUSED postgres://user:hunter2@db:5432 token=supersecret at /Users/alice/work/secret-repo/.env",
				Command:    "oberth logs api --once",
			}},
			Loops: []runtimecore.LoopSignal{{Kind: runtimecore.LoopCrashLoop, Service: "api", Count: 4, Summary: "4 crashes"}},
		},
	}
}

func TestEvaluateWorldSendsOnlyRedactedCausalFacts(t *testing.T) {
	var seen string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = string(raw)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + jsonString(decisionJSON) + `}}]}`))
	}))
	defer server.Close()
	triage, err := (Client{
		Endpoint: server.URL, Model: "m", APIKey: "k", Protocol: ProtocolOpenAIChat,
		HTTPClient: server.Client(), IncludeErrorLine: true,
	}).EvaluateWorld(context.Background(), triageWorld(), TaskContext{
		Task: "Fix the API", AuthorizedOptions: []string{"inspect_logs", "restart_service", "continue_without_action"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if triage.NeedsHuman != 0.12 || triage.Summary == "" {
		t.Fatalf("triage = %+v", triage)
	}
	for _, forbidden := range []string{
		"/Users/alice", "secret-repo", "hunter2", "supersecret", "leaky-command", "127.0.0.1:5432",
	} {
		if strings.Contains(seen, forbidden) {
			t.Errorf("payload leaked %q: %s", forbidden, seen)
		}
	}
	for _, want := range []string{"worktree_id", "root_causes", "port_holder", "code_stale", "crash_loop", "first_error"} {
		if !strings.Contains(seen, want) {
			t.Errorf("payload missing %q: %s", want, seen)
		}
	}
}

func TestEvaluateWorldOmitsFirstErrorWhenErrorLinesAreOff(t *testing.T) {
	var seen string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = string(raw)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + jsonString(decisionJSON) + `}}]}`))
	}))
	defer server.Close()
	_, err := (Client{
		Endpoint: server.URL, Model: "m", APIKey: "k", Protocol: ProtocolOpenAIChat,
		HTTPClient: server.Client(), IncludeErrorLine: false,
	}).EvaluateWorld(context.Background(), triageWorld(), TaskContext{Task: "Fix the API"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seen, "first_error") || strings.Contains(seen, "ECONNREFUSED") {
		t.Fatalf("error line travelled with error lines off: %s", seen)
	}
}

func TestEvaluateWorldRejectsAnOptionTheWorldDoesNotOffer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a rejected authorization still contacted the provider")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	_, err := (Client{Endpoint: server.URL, Model: "m", APIKey: "k", Protocol: ProtocolOpenAIChat, HTTPClient: server.Client()}).
		EvaluateWorld(context.Background(), runtimecore.World{
			Revision: "rev", Worktree: runtimecore.WorldIdentity{Root: "/tmp/repo"},
		}, TaskContext{Task: "fix", AuthorizedOptions: []string{"stop_service"}})
	if err == nil || !strings.Contains(err.Error(), "offered by this event") {
		t.Fatalf("err = %v, want an authorization rejection", err)
	}
}

func TestRedactLineRemovesSecretsAndPathsAndBoundsLength(t *testing.T) {
	line := "  failed  connecting api_key=abc123secret to /srv/app/config.yaml and C:\\Users\\alice\\app.env " + strings.Repeat("x", 400)
	got := RedactLine(line)
	if strings.Contains(got, "abc123secret") || strings.Contains(got, "/srv/app") || strings.Contains(got, `C:\Users`) {
		t.Fatalf("redacted line still carries private text: %q", got)
	}
	if strings.Contains(got, "\n") || len([]rune(got)) > 240 {
		t.Fatalf("redacted line is not one bounded line: %d runes", len([]rune(got)))
	}
}

func TestProtocolAndModeDefaults(t *testing.T) {
	if NormalizeProtocol("") != string(ProtocolDecisions) || !KnownProtocol("") {
		t.Fatal("empty protocol is not the decisions default")
	}
	if NormalizeMode("") != string(ModeAssist) || !KnownMode("") {
		t.Fatal("empty mode is not the assist default")
	}
	if KnownProtocol("grpc") || KnownMode("autopilot") {
		t.Fatal("an unknown protocol or mode was accepted")
	}
	cfg := LocalConfig{}
	if !cfg.AllowsErrorLine() || cfg.EffectiveProtocol() != string(ProtocolDecisions) || cfg.EffectiveMode() != string(ModeAssist) {
		t.Fatalf("empty config defaults = %+v", cfg)
	}
	off := false
	cfg.IncludeErrorLine = &off
	if cfg.AllowsErrorLine() {
		t.Fatal("an explicit false still allowed error lines")
	}
}

func TestPresetsCarryTheirProtocol(t *testing.T) {
	cases := map[string]Protocol{
		"openrouter": ProtocolDecisions,
		"typesafe":   ProtocolDecisions,
		"openai":     ProtocolOpenAIChat,
		"anthropic":  ProtocolAnthropic,
		"ollama":     ProtocolOpenAIChat,
	}
	for name, want := range cases {
		got := PresetProtocol(name)
		if got != string(want) {
			t.Errorf("%s protocol = %q, want %q", name, got, want)
		}
		filled := (EnableOptions{Preset: name}).SettingsFrom(LocalConfig{})
		if filled.Protocol != string(want) {
			t.Errorf("%s settings protocol = %q, want %q", name, filled.Protocol, want)
		}
	}
	if PresetProtocol("acme") != "" {
		t.Fatal("an unknown preset offered a protocol")
	}
}

func TestLoopbackEndpointNeedsNoKeyAndRemoteStillDoes(t *testing.T) {
	local := Resolve(ResolveInput{
		Settings: LocalConfig{
			Enabled: true, Preset: "ollama", Protocol: string(ProtocolOpenAIChat),
			Endpoint: "http://127.0.0.1:11434/v1/chat/completions", Model: "qwen2.5:7b",
		},
		SettingsPresent: true,
	})
	if !local.Enabled || local.Reason != "" {
		t.Fatalf("loopback settings = %+v", local)
	}
	if local.KeySource != SourceLoopback || local.Key == "" {
		t.Fatalf("loopback key = %q from %q", local.Key, local.KeySource)
	}
	if _, err := local.Client(); err != nil {
		t.Fatalf("loopback client: %v", err)
	}
	storedPlaceholder := Resolve(ResolveInput{
		Settings: LocalConfig{
			Enabled: true, Preset: "ollama", Protocol: string(ProtocolOpenAIChat),
			Endpoint: "http://127.0.0.1:11434/v1/chat/completions", Model: "qwen2.5:7b",
			APIKey: LoopbackKeyPlaceholder,
		},
		SettingsPresent: true,
	})
	if storedPlaceholder.KeySource != SourceLoopback {
		t.Fatalf("stored loopback placeholder key_source = %q, want %q", storedPlaceholder.KeySource, SourceLoopback)
	}
	remote := Resolve(ResolveInput{
		Settings: LocalConfig{
			Enabled: true, Endpoint: "https://api.example/v1/chat/completions", Model: "m",
		},
		SettingsPresent: true,
	})
	if remote.Reason != ReasonNoKey {
		t.Fatalf("remote without a key = %+v, want no_key", remote)
	}
	if _, err := remote.Client(); err == nil {
		t.Fatal("a remote endpoint produced a client without a key")
	}
}

func TestResolveRejectsUnknownProtocolOrMode(t *testing.T) {
	for _, in := range []ResolveInput{
		{OneShot: "1", Endpoint: "https://api.example/v1", Model: "m", Key: "k", Protocol: "grpc"},
		{OneShot: "1", Endpoint: "https://api.example/v1", Model: "m", Key: "k", Mode: "autopilot"},
	} {
		got := Resolve(in)
		if got.Enabled || got.Reason != ReasonInvalidConfig {
			t.Fatalf("resolved = %+v, want invalid_config", got)
		}
		if got.Key != "" {
			t.Fatal("an invalid configuration kept the key")
		}
	}
}

func TestResolvedClientCarriesProtocolAndModes(t *testing.T) {
	got := Resolve(ResolveInput{
		OneShot: "1", Endpoint: "https://api.example/v1", Model: "m", Key: "k",
		Protocol: "openai_chat", Mode: "reconcile",
	})
	client, err := got.Client()
	if err != nil {
		t.Fatal(err)
	}
	if client.Protocol != ProtocolOpenAIChat || client.Mode != ModeReconcile {
		t.Fatalf("client = %+v", client)
	}
	state := got.RedactedState()
	if state["protocol"] != "openai_chat" || state["mode"] != "reconcile" || state["include_error_line"] != true {
		t.Fatalf("redacted state = %+v", state)
	}
}

// A local runtime with no key must be usable through the client, and a remote
// one without a key must stay refused. This is the one place the two differ.
func TestClientAcceptsAbsentKeyOnlyOnLoopback(t *testing.T) {
	if _, err := (Client{Endpoint: "http://127.0.0.1:11434/v1/chat/completions", Model: "m", Protocol: ProtocolOpenAIChat}).normalized(); err != nil {
		t.Fatalf("loopback client: %v", err)
	}
	if _, err := (Client{Endpoint: "https://api.example/v1/chat", Model: "m", Protocol: ProtocolOpenAIChat}).normalized(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("remote client without a key: %v", err)
	}
}

func TestAssessmentUrgencyAndSummaryBounds(t *testing.T) {
	artifact := protocolArtifact(t)
	bad := 1.5
	if err := attn.AttachAssessment(&artifact, attn.Assessment{Urgency: &bad}); err == nil {
		t.Fatal("an out-of-range urgency was attached")
	}
	good := 0.5
	long := strings.Repeat("word ", 80)
	if err := attn.AttachAssessment(&artifact, attn.Assessment{Urgency: &good, Summary: long}); err != nil {
		t.Fatal(err)
	}
	if len([]rune(artifact.Assessment.Summary)) > attn.MaxSummaryRunes || strings.Contains(artifact.Assessment.Summary, "\n") {
		t.Fatalf("summary was not bounded: %q", artifact.Assessment.Summary)
	}
}
