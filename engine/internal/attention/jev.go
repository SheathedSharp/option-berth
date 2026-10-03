package attention

// This file contains the optional Jev adapter. It is intentionally separate
// from status and the daemon: a missing key, a network failure, or an invalid
// model response must never change the runtime facts or the CLI control path.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	DefaultJevEndpoint        = "https://api.typesafe.ai/v1/systemone"
	DefaultJevModel           = "jev-latest"
	DefaultOpenRouterEndpoint = "https://openrouter.ai/api/alpha/decisions"
	DefaultOpenRouterModel    = "typesafe/jev-1.13"
	DefaultJevTimeout         = 10 * time.Second
	maxTaskBytes              = 2000
	maxReasonBytes            = 256
	maxServiceBytes           = 128
)

// ErrUnavailable means that no assessment was made. Callers should leave the
// fact-only artifact untouched and let the agent ask the user directly.
var ErrUnavailable = errors.New("attention: Jev unavailable")

// ErrStale means that the artifact changed or expired while the remote call
// was in flight. The response must not be attached to a newer event.
var ErrStale = errors.New("attention: artifact became stale")

// TaskContext is supplied by the development agent for each assessment. The
// adapter requires both a task summary and an explicit allow-list of options;
// it never infers permission from the artifact or from model output.
type TaskContext struct {
	Task              string   `json:"task"`
	AuthorizedOptions []string `json:"authorized_options"`
}

// FilterAuthorizedOptions validates the agent's explicit option vocabulary and
// returns only the options offered by the current deterministic event. A broad
// allow-list is useful to a long-running agent, but event candidates never
// become permission merely because they appear in the artifact.
func FilterAuthorizedOptions(options []Option, authorized []string) ([]string, error) {
	offered := make(map[string]bool, len(options))
	for _, option := range options {
		if option.ID != "" {
			offered[option.ID] = true
		}
	}
	seen := make(map[string]bool, len(authorized))
	result := make([]string, 0, len(authorized))
	for _, raw := range authorized {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		if !KnownOptionID(id) {
			return nil, fmt.Errorf("attention: authorization contains unknown option %q", id)
		}
		seen[id] = true
		if offered[id] {
			result = append(result, id)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("attention: at least one authorized option must be offered by this event")
	}
	sort.Strings(result)
	return result, nil
}

func (t TaskContext) validate(options []Option) ([]string, error) {
	t.Task = strings.TrimSpace(t.Task)
	if t.Task == "" {
		return nil, errors.New("attention: task context is required")
	}
	if len([]byte(t.Task)) > maxTaskBytes {
		return nil, fmt.Errorf("attention: task context exceeds %d bytes", maxTaskBytes)
	}
	return FilterAuthorizedOptions(options, t.AuthorizedOptions)
}

// Client calls the official TypeSafe System One endpoint. HTTPClient is
// injectable for tests; nil uses http.DefaultClient.
type Client struct {
	Endpoint   string
	APIKey     string
	Model      string
	HTTPClient *http.Client
	Timeout    time.Duration
}

func (c Client) normalized() (Client, error) {
	c.APIKey = strings.TrimSpace(c.APIKey)
	if c.APIKey == "" {
		return Client{}, ErrUnavailable
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		c.Endpoint = DefaultJevEndpoint
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Client{}, fmt.Errorf("attention: Jev endpoint must be an HTTPS URL")
	}
	if strings.TrimSpace(c.Model) == "" {
		c.Model = DefaultJevModel
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultJevTimeout
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	return c, nil
}

// jevRequest and jevResponse mirror TypeSafe's documented HTTP contract. The
// state sent here is deliberately a small, redacted summary; log contents and
// environment variables never cross this boundary.
type jevRequest struct {
	State     jevState               `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]jevQuestion `json:"questions"`
}

type jevState struct {
	Task              string     `json:"task"`
	WorktreeID        string     `json:"worktree_id"`
	Branch            string     `json:"branch,omitempty"`
	StateRevision     string     `json:"state_revision"`
	Event             jevEvent   `json:"event"`
	Events            []jevEvent `json:"events,omitempty"`
	EvidenceRefs      []string   `json:"evidence_refs"`
	CandidateOptions  []string   `json:"candidate_options"`
	AuthorizedOptions []string   `json:"authorized_options"`
}

type jevEvent struct {
	Kind         string `json:"kind"`
	Service      string `json:"service,omitempty"`
	Machine      string `json:"machine,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Port         int    `json:"port,omitempty"`
	DeclaredPort *int   `json:"declared_port,omitempty"`
	ActualPort   *int   `json:"actual_port,omitempty"`
	Count        int    `json:"count,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
}

type jevQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type jevResponse struct {
	Model   string               `json:"model"`
	Answers map[string]jevAnswer `json:"answers"`
}

type jevAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// Result contains the validated, typed recommendation and the task relevance
// value that was used to construct it. Usage is not currently persisted in the
// artifact; keeping it here lets a CLI report it without adding facts.
type Result struct {
	Assessment   Assessment
	TaskRelevant float64
	Model        string
}

// Evaluate sends two Noul questions and, when there is more than one
// authorized option, one Choice question. The caller must persist the result
// with Persist; this method never mutates an artifact or executes an action.
func (c Client) Evaluate(ctx context.Context, artifact Artifact, task TaskContext) (Result, error) {
	client, err := c.normalized()
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(artifact.EventID) == "" || strings.TrimSpace(artifact.StateRevision) == "" {
		return Result{}, errors.New("attention: artifact is missing identity")
	}
	if strings.TrimSpace(artifact.WorktreeRoot) == "" || artifact.Event.Kind == EventNone {
		return Result{}, errors.New("attention: artifact is missing event identity")
	}
	if _, err := parseFreshUntil(artifact); err != nil {
		return Result{}, err
	}
	options, err := task.validate(artifact.Options)
	if err != nil {
		return Result{}, err
	}
	state := safeState(artifact, task, options)
	questions := map[string]jevQuestion{
		"task_relevant": {
			Type:         "noul",
			Instructions: "Does this deterministic runtime event materially affect the development task in state.task?",
			Criteria: map[string]string{
				"true":  "The event can block, invalidate, or change the task's next useful step.",
				"false": "The event is unrelated to the task or can safely be ignored while it proceeds.",
			},
		},
		"needs_human": {
			Type:         "noul",
			Instructions: "Given the task, event, evidence references, and explicitly authorized options, must the agent ask the user to choose before proceeding?",
			Criteria: map[string]string{
				"true":  "The next step involves a user tradeoff, ambiguity, or a side effect that the agent must not choose on its own.",
				"false": "The agent can safely continue with a documented, reversible, already authorized inspection.",
			},
		},
	}
	if len(options) > 1 {
		criteria := make(map[string]string, len(options))
		for _, id := range options {
			criteria[id] = optionDescription(id)
		}
		questions["next_option"] = jevQuestion{
			Type:         "choice",
			Instructions: "Which authorized, pre-defined next step should the agent present first to the user?",
			Criteria:     criteria,
		}
	}
	payload := jevRequest{State: state, Model: client.Model, Questions: questions}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("attention: encode Jev request: %w", err)
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > client.Timeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, client.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("attention: create Jev request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+client.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return Result{}, fmt.Errorf("%w: read response: %v", ErrUnavailable, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not return the response body: providers may echo state or account
		// details and errors should never become agent-visible evidence.
		return Result{}, fmt.Errorf("%w: Jev HTTP status %d", ErrUnavailable, resp.StatusCode)
	}
	var decoded jevResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return Result{}, fmt.Errorf("%w: invalid Jev response", ErrUnavailable)
	}
	result, err := validateResponse(decoded, options, client.Model)
	if err != nil {
		return Result{}, err
	}
	result.Assessment.ContextID = assessmentContextID(task, options)
	return result, nil
}

func assessmentContextID(task TaskContext, authorized []string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00", strings.TrimSpace(task.Task))
	for _, option := range authorized {
		_, _ = fmt.Fprintf(h, "%s\x00", option)
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// CachedResult reuses a validated assessment only for the same live event and
// explicit task/option context. A changed task must be assessed again.
func CachedResult(artifact Artifact, task TaskContext) (Result, bool) {
	options, err := task.validate(artifact.Options)
	if err != nil || artifact.Assessment == nil || !IsFresh(artifact, time.Time{}) || artifact.RecoveredAt != "" {
		return Result{}, false
	}
	a := *artifact.Assessment
	if a.ContextID != assessmentContextID(task, options) || a.TaskRelevant == nil {
		return Result{}, false
	}
	if err := AttachAssessment(&artifact, a); err != nil {
		return Result{}, false
	}
	return Result{Assessment: a, TaskRelevant: *a.TaskRelevant, Model: a.Model}, true
}

func safeState(a Artifact, task TaskContext, authorized []string) jevState {
	refs := make([]string, 0, len(a.Evidence))
	for _, evidence := range a.Evidence {
		if ref := safeRef(evidence.Ref); ref != "" {
			refs = append(refs, ref)
		}
	}
	events := a.Events
	if len(events) == 0 {
		events = []Event{a.Event}
	}
	jevEvents := make([]jevEvent, 0, len(events))
	for _, event := range events {
		jevEvents = append(jevEvents, safeEvent(event))
	}
	firstEvent := safeEvent(a.Event)
	if len(jevEvents) > 0 {
		firstEvent = jevEvents[0]
	}
	return jevState{
		Task:              redactText(task.Task, maxTaskBytes),
		WorktreeID:        hashWorktree(a.WorktreeRoot),
		Branch:            redactText(redactIdentifier(a.Branch, maxServiceBytes), maxServiceBytes),
		StateRevision:     redactIdentifier(a.StateRevision, 128),
		Event:             firstEvent,
		Events:            jevEvents,
		EvidenceRefs:      refs,
		CandidateOptions:  optionIDs(a.Options),
		AuthorizedOptions: append([]string(nil), authorized...),
	}
}

func safeEvent(event Event) jevEvent {
	return jevEvent{
		Kind:         redactText(redactIdentifier(string(event.Kind), maxReasonBytes), maxReasonBytes),
		Service:      redactText(redactIdentifier(event.Service, maxServiceBytes), maxServiceBytes),
		Machine:      redactText(redactIdentifier(event.Machine, maxServiceBytes), maxServiceBytes),
		Reason:       redactText(redactIdentifier(event.Reason, maxReasonBytes), maxReasonBytes),
		Port:         event.Port,
		DeclaredPort: event.DeclaredPort,
		ActualPort:   event.ActualPort,
		Count:        event.Count,
		RunID:        redactIdentifier(event.RunID, maxServiceBytes),
		ExitCode:     event.ExitCode,
	}
}

func optionIDs(options []Option) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		if option.ID != "" {
			ids = append(ids, option.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func optionDescription(id string) string {
	if policy, ok := OptionPolicyFor(id); ok {
		if policy.RequiresExplicitChoice {
			return policy.Description + " This is a side effect requiring the user's choice."
		}
		return policy.Description + " No runtime mutation."
	}
	return "A pre-defined option in the attention artifact."
}

func validateResponse(response jevResponse, options []string, configuredModel string) (Result, error) {
	if response.Answers == nil {
		return Result{}, fmt.Errorf("%w: Jev response has no answers", ErrUnavailable)
	}
	required := []string{"task_relevant", "needs_human"}
	if len(options) > 1 {
		required = append(required, "next_option")
	}
	for _, key := range required {
		if _, ok := response.Answers[key]; !ok {
			return Result{}, fmt.Errorf("%w: Jev response omitted %s", ErrUnavailable, key)
		}
	}
	relevant, err := validateNoul(response.Answers["task_relevant"], "task_relevant")
	if err != nil {
		return Result{}, err
	}
	needsHuman, err := validateNoul(response.Answers["needs_human"], "needs_human")
	if err != nil {
		return Result{}, err
	}
	assessment := Assessment{NeedsHuman: &needsHuman, TaskRelevant: &relevant}
	if len(options) == 1 {
		assessment.NextOption = options[0]
		assessment.Probabilities = map[string]float64{options[0]: 1}
	} else {
		choice := response.Answers["next_option"]
		if choice.Type != "choice" || !finiteProbability(choice.Confidence) || choice.Confidence == nil {
			return Result{}, fmt.Errorf("%w: invalid next_option confidence", ErrUnavailable)
		}
		if !contains(options, choice.Choice) {
			return Result{}, fmt.Errorf("%w: Jev selected unauthorized option %q", ErrUnavailable, choice.Choice)
		}
		probabilities, err := validateChoiceProbabilities(choice.Probabilities, options)
		if err != nil {
			return Result{}, err
		}
		assessment.NextOption = choice.Choice
		assessment.Probabilities = probabilities
		assessment.Confidence = choice.Confidence
	}
	assessment.Model = strings.TrimSpace(response.Model)
	if assessment.Model == "" {
		assessment.Model = configuredModel
	}
	return Result{Assessment: assessment, TaskRelevant: relevant, Model: assessment.Model}, nil
}

func validateNoul(answer jevAnswer, key string) (float64, error) {
	if answer.Type != "noul" || answer.Noul == nil || !finite(*answer.Noul) {
		return 0, fmt.Errorf("%w: invalid %s answer", ErrUnavailable, key)
	}
	return *answer.Noul, nil
}

func validateChoiceProbabilities(values map[string]float64, options []string) (map[string]float64, error) {
	if len(values) != len(options) {
		return nil, fmt.Errorf("%w: choice probabilities do not cover authorized options", ErrUnavailable)
	}
	total := 0.0
	for _, option := range options {
		value, ok := values[option]
		if !ok || !finite(value) {
			return nil, fmt.Errorf("%w: invalid probability for %s", ErrUnavailable, option)
		}
		total += value
	}
	if math.Abs(total-1) > 0.01 {
		return nil, fmt.Errorf("%w: choice probabilities sum to %.4f", ErrUnavailable, total)
	}
	result := make(map[string]float64, len(options))
	for _, option := range options {
		result[option] = values[option]
	}
	return result, nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
func finiteProbability(value *float64) bool { return value != nil && finite(*value) }
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func parseFreshUntil(a Artifact) (time.Time, error) {
	if strings.TrimSpace(a.FreshUntil) == "" {
		return time.Time{}, errors.New("attention: artifact has no freshness deadline")
	}
	fresh, err := time.Parse(time.RFC3339Nano, a.FreshUntil)
	if err != nil {
		return time.Time{}, fmt.Errorf("attention: invalid freshness deadline: %w", err)
	}
	if !time.Now().UTC().Before(fresh) {
		return time.Time{}, ErrStale
	}
	return fresh, nil
}

// Persist re-reads the artifact after the remote call, ensuring an assessment
// can only be attached to the same event and revision. It writes through the
// existing attention writer and therefore preserves atomic 0600 publication.
func Persist(path string, original Artifact, result Result) error {
	if err := validateAssessment(&original, result.Assessment); err != nil {
		return err
	}
	if _, err := parseFreshUntil(original); err != nil {
		return err
	}
	return CompareAndSwap(path, original.EventID, original.StateRevision, func(latest *Artifact) error {
		if latest.WorktreeRoot != original.WorktreeRoot || latest.Branch != original.Branch {
			return ErrStale
		}
		if _, err := parseFreshUntil(*latest); err != nil {
			return err
		}
		return AttachAssessment(latest, result.Assessment)
	})
}

func validateAssessment(artifact *Artifact, assessment Assessment) error {
	if err := AttachAssessment(artifact, assessment); err != nil {
		return err
	}
	return nil
}

var (
	secretPattern = regexp.MustCompile(`(?i)(bearer\s+|api[_-]?key|token|password|secret)\s*[:=]\s*[^\s,;]+`)
	pathPattern   = regexp.MustCompile(`(?:/Users/[^\s"']+|/home/[^\s"']+|/private/tmp/[^\s"']+|/tmp/[^\s"']+|/var/tmp/[^\s"']+|/var/folders/[^\s"']+)`)
	refPattern    = regexp.MustCompile(`^[A-Za-z0-9._\[\]:-]+$`)
)

func redactText(value string, limit int) string {
	value = secretPattern.ReplaceAllString(value, "$1<redacted>")
	value = pathPattern.ReplaceAllString(value, "<path>")
	if len([]byte(value)) > limit {
		value = string([]byte(value)[:limit])
	}
	return value
}

func redactIdentifier(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len([]byte(value)) > limit {
		value = string([]byte(value)[:limit])
	}
	return value
}

func safeRef(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len([]byte(value)) > 256 || !refPattern.MatchString(value) {
		return "<redacted>"
	}
	return value
}

func hashWorktree(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(sum[:])[:24]
}

// ReadTaskContext reads a strict JSON context file for the standalone command.
func ReadTaskContext(path string) (TaskContext, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TaskContext{}, err
	}
	var task TaskContext
	if err := json.Unmarshal(data, &task); err != nil {
		return TaskContext{}, fmt.Errorf("attention: decode task context: %w", err)
	}
	return task, nil
}
