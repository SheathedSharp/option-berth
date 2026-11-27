package model

// This file contains the optional model adapter. It is intentionally separate
// from status and the runtime_service: a missing key, a network failure, or an invalid
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

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
)

const (
	DefaultTimeout  = 10 * time.Second
	maxTaskBytes    = 2000
	maxReasonBytes  = 256
	maxServiceBytes = 128
)

// ErrUnavailable means that no assessment was made. Callers should leave the
// fact-only artifact untouched and let the agent ask the user directly.
var ErrUnavailable = errors.New("attn: model unavailable")

// ErrStale is the artifact writer's sentinel, re-exported for callers that only
// import this package: a response that arrived after the event changed must be
// discarded, not attached.
var ErrStale = attn.ErrStale

// TaskContext is supplied by the development agent for each assessment. The
// adapter requires both a task summary and an explicit allow-list of options;
// it never infers permission from the artifact or from model output.
type TaskContext struct {
	Task              string   `json:"task"`
	AuthorizedOptions []string `json:"authorized_options"`
	Services          []string `json:"services,omitempty"`
	RunIDs            []string `json:"run_ids,omitempty"`
}

// FilterAuthorizedOptions validates the agent's explicit option vocabulary and
// returns only the options offered by the current deterministic event. A broad
// allow-list is useful to a long-running agent, but event candidates never
// become permission merely because they appear in the artifact.
func FilterAuthorizedOptions(options []attn.Option, authorized []string) ([]string, error) {
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
		if !attn.KnownOptionID(id) {
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

func (t TaskContext) validate(options []attn.Option) ([]string, error) {
	t.Task = strings.TrimSpace(t.Task)
	if t.Task == "" {
		return nil, errors.New("attention: task context is required")
	}
	if len([]byte(t.Task)) > maxTaskBytes {
		return nil, fmt.Errorf("attention: task context exceeds %d bytes", maxTaskBytes)
	}
	return FilterAuthorizedOptions(options, t.AuthorizedOptions)
}

// Client calls one configured endpoint. HTTPClient is injectable for tests;
// nil uses http.DefaultClient.
type Client struct {
	Endpoint string
	APIKey   string
	Model    string
	// Protocol is the wire shape the endpoint speaks. Empty means the
	// decisions schema every provider before this field spoke.
	Protocol Protocol
	// IncludeErrorLine says whether one redacted first-error line may travel
	// with a world triage. It has no effect on Evaluate, whose payload never
	// carried log text.
	IncludeErrorLine bool
	// Mode is carried so a caller can report which autonomy produced a
	// result. The client itself never acts on it.
	Mode       Mode
	HTTPClient *http.Client
	Timeout    time.Duration
}

func (c Client) normalized() (Client, error) {
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Model = strings.TrimSpace(c.Model)
	if c.Endpoint == "" || c.Model == "" {
		if c.APIKey == "" {
			return Client{}, WithReason(ReasonNoKey, ErrUnavailable)
		}
		return Client{}, WithReason(ReasonInvalidConfig, ErrUnavailable)
	}
	if err := validateEndpoint(c.Endpoint); err != nil {
		return Client{}, WithReason(ReasonInvalidConfig, fmt.Errorf("attn: %w", err))
	}
	if c.APIKey == "" {
		// A loopback endpoint is a runtime on this machine; it is configured
		// without a secret on purpose. Every other endpoint still needs one.
		if !loopbackEndpoint(c.Endpoint) {
			return Client{}, WithReason(ReasonNoKey, ErrUnavailable)
		}
		c.APIKey = loopbackKey
	}
	c.Protocol = Protocol(NormalizeProtocol(string(c.Protocol)))
	if !KnownProtocol(string(c.Protocol)) {
		return Client{}, WithReason(ReasonInvalidConfig, ErrUnavailable)
	}
	c.Mode = Mode(NormalizeMode(string(c.Mode)))
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	return c, nil
}

// loopbackEndpoint reports whether an endpoint is a plain-HTTP address on this
// machine. Only those may be configured without a key.
func loopbackEndpoint(endpoint string) bool {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

// apiRequest and apiResponse mirror TypeSafe's documented HTTP contract. The
// state sent here is deliberately a small, redacted summary; log contents and
// environment variables never cross this boundary.
type apiRequest struct {
	State     apiState               `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]apiQuestion `json:"questions"`
}

type apiState struct {
	Task              string     `json:"task"`
	WorktreeID        string     `json:"worktree_id"`
	Branch            string     `json:"branch,omitempty"`
	StateRevision     string     `json:"state_revision"`
	Event             apiEvent   `json:"event"`
	Events            []apiEvent `json:"events,omitempty"`
	EvidenceRefs      []string   `json:"evidence_refs"`
	CandidateOptions  []string   `json:"candidate_options"`
	AuthorizedOptions []string   `json:"authorized_options"`
}

type apiEvent struct {
	Kind         string `json:"kind"`
	Service      string `json:"service,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Port         int    `json:"port,omitempty"`
	DeclaredPort *int   `json:"declared_port,omitempty"`
	ActualPort   *int   `json:"actual_port,omitempty"`
	Count        int    `json:"count,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
}

type apiQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type apiResponse struct {
	Model   string               `json:"model"`
	Answers map[string]apiAnswer `json:"answers"`
}

type apiAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// decisionPayload is the protocol-independent answer shape every structured
// protocol parses into before validation. One validator for all three is what
// keeps a provider from reaching the artifact through a bound another provider
// checks: the wire schema differs, the rule does not.
type decisionPayload struct {
	TaskRelevant  *float64           `json:"task_relevant,omitempty"`
	NeedsHuman    *float64           `json:"needs_human,omitempty"`
	Urgency       *float64           `json:"urgency,omitempty"`
	NextOption    string             `json:"next_option,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Summary       string             `json:"summary,omitempty"`
	Model         string             `json:"model,omitempty"`
}

// Result contains the validated, typed recommendation and the task relevance
// value that was used to construct it. Usage is not currently persisted in the
// artifact; keeping it here lets a CLI report it without adding facts.
type Result struct {
	Assessment   attn.Assessment
	TaskRelevant float64
	Model        string
}

// Evaluate sends two Noul questions and, when there is more than one
// authorized option, one Choice question. The caller must persist the result
// with Persist; this method never mutates an artifact or executes an action.
func (c Client) Evaluate(ctx context.Context, artifact attn.Artifact, task TaskContext) (Result, error) {
	return c.EvaluateObserved(ctx, artifact, task, nil)
}

// EvaluateObserved reports an actual transport attempt, after validation.
// The observer is local accounting only and receives no request contents.
func (c Client) EvaluateObserved(ctx context.Context, artifact attn.Artifact, task TaskContext, attempted func()) (Result, error) {
	client, err := c.normalized()
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(artifact.EventID) == "" || strings.TrimSpace(artifact.StateRevision) == "" {
		return Result{}, errors.New("attention: artifact is missing identity")
	}
	if strings.TrimSpace(artifact.WorktreeRoot) == "" || artifact.Event.Kind == attn.EventNone {
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
	questions := map[string]apiQuestion{
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
		// urgency is optional: a provider that does not answer score questions
		// still produces a usable assessment, and a provider that does can say
		// how soon the next step stops being optional.
		"urgency": {
			Type:         "score",
			Instructions: "How soon does this event stop the development task from proceeding usefully, if nothing is done?",
			Criteria: map[string]string{
				"0": "the task is unaffected and can continue for now",
				"1": "the task cannot proceed at all until this is resolved",
			},
		},
	}
	if len(options) > 1 {
		criteria := make(map[string]string, len(options))
		for _, id := range options {
			criteria[id] = optionDescription(id)
		}
		questions["next_option"] = apiQuestion{
			Type:         "choice",
			Instructions: "Which authorized, pre-defined next step should the agent present first to the user?",
			Criteria:     criteria,
		}
	}
	body, err := encodeDecisionRequest(client.Protocol, client.Model, state, questions)
	if err != nil {
		return Result{}, err
	}
	responseBody, err := client.exchange(ctx, body, attempted)
	if err != nil {
		return Result{}, err
	}
	result, err := decisionResult(client.Protocol, responseBody, options, client.Model)
	if err != nil {
		return Result{}, err
	}
	result.Assessment.ContextID = assessmentContextID(task, options)
	return result, nil
}

// exchange posts one already-encoded request with the headers its protocol
// needs and returns the bounded response body. It is the only place a provider
// is contacted, so the timeout rule, the status classification and the
// request-events hook have exactly one implementation.
func (c Client) exchange(ctx context.Context, body []byte, attempted func()) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > c.Timeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("attn: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	switch Protocol(NormalizeProtocol(string(c.Protocol))) {
	case ProtocolAnthropic:
		req.Header.Set("x-api-key", c.APIKey)
		req.Header.Set("anthropic-version", anthropicVersion)
	default:
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if err := ctx.Err(); err != nil {
		return nil, WithReason(reasonFromTransport(ctx, err), err)
	}
	if attempted != nil {
		attempted()
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		// A timeout, an unreachable host and a cancelled context are three
		// different next steps for the person, so classify before wrapping.
		return nil, WithReason(reasonFromTransport(ctx, err), fmt.Errorf("%w: %v", ErrUnavailable, err))
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, WithReason(reasonFromTransport(ctx, readErr), fmt.Errorf("%w: read response: %v", ErrUnavailable, readErr))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not return the response body: providers may echo state or account
		// details and errors should never become agent-visible evidence.
		return nil, WithReason(reasonFromStatus(resp.StatusCode), fmt.Errorf("%w: HTTP status %d", ErrUnavailable, resp.StatusCode))
	}
	return responseBody, nil
}

func assessmentContextID(task TaskContext, authorized []string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00", strings.TrimSpace(task.Task))
	for _, targets := range [][]string{task.Services, task.RunIDs} {
		sorted := append([]string(nil), targets...)
		sort.Strings(sorted)
		_, _ = fmt.Fprint(h, "scope\x00")
		for _, target := range sorted {
			_, _ = fmt.Fprintf(h, "%s\x00", target)
		}
	}
	for _, option := range authorized {
		_, _ = fmt.Fprintf(h, "%s\x00", option)
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// CachedResult reuses a validated assessment only for the same live event and
// explicit task/option context. A changed task must be assessed again.
func CachedResult(artifact attn.Artifact, task TaskContext) (Result, bool) {
	options, err := task.validate(artifact.Options)
	if err != nil || artifact.Assessment == nil || !attn.IsFresh(artifact, time.Time{}) || artifact.RecoveredAt != "" {
		return Result{}, false
	}
	a := *artifact.Assessment
	if a.ContextID != assessmentContextID(task, options) || a.TaskRelevant == nil {
		return Result{}, false
	}
	if err := attn.AttachAssessment(&artifact, a); err != nil {
		return Result{}, false
	}
	return Result{Assessment: a, TaskRelevant: *a.TaskRelevant, Model: a.Model}, true
}

func safeState(a attn.Artifact, task TaskContext, authorized []string) apiState {
	refs := make([]string, 0, len(a.Evidence))
	for _, evidence := range a.Evidence {
		if ref := safeRef(evidence.Ref); ref != "" {
			refs = append(refs, ref)
		}
	}
	events := a.Events
	if len(events) == 0 {
		events = []attn.Event{a.Event}
	}
	apiEvents := make([]apiEvent, 0, len(events))
	for _, event := range events {
		apiEvents = append(apiEvents, safeEvent(event))
	}
	firstEvent := safeEvent(a.Event)
	if len(apiEvents) > 0 {
		firstEvent = apiEvents[0]
	}
	return apiState{
		Task:              redactText(task.Task, maxTaskBytes),
		WorktreeID:        hashWorktree(a.WorktreeRoot),
		Branch:            redactText(redactIdentifier(a.Branch, maxServiceBytes), maxServiceBytes),
		StateRevision:     redactIdentifier(a.StateRevision, 128),
		Event:             firstEvent,
		Events:            apiEvents,
		EvidenceRefs:      refs,
		CandidateOptions:  optionIDs(a.Options),
		AuthorizedOptions: append([]string(nil), authorized...),
	}
}

func safeEvent(event attn.Event) apiEvent {
	return apiEvent{
		Kind:         redactText(redactIdentifier(string(event.Kind), maxReasonBytes), maxReasonBytes),
		Service:      redactText(redactIdentifier(event.Service, maxServiceBytes), maxServiceBytes),
		Reason:       redactText(redactIdentifier(event.Reason, maxReasonBytes), maxReasonBytes),
		Port:         event.Port,
		DeclaredPort: event.DeclaredPort,
		ActualPort:   event.ActualPort,
		Count:        event.Count,
		RunID:        redactIdentifier(event.RunID, maxServiceBytes),
		ExitCode:     event.ExitCode,
	}
}

func optionIDs(options []attn.Option) []string {
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
	if policy, ok := attn.OptionPolicyFor(id); ok {
		if policy.RequiresExplicitChoice {
			return policy.Description + " This is a side effect requiring the user's choice."
		}
		return policy.Description + " No runtime mutation."
	}
	return "A pre-defined option in the attention artifact."
}

func validateResponse(response apiResponse, options []string, configuredModel string) (Result, error) {
	if response.Answers == nil {
		return Result{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: model response has no answers", ErrUnavailable))
	}
	required := []string{"task_relevant", "needs_human"}
	if len(options) > 1 {
		required = append(required, "next_option")
	}
	for _, key := range required {
		if _, ok := response.Answers[key]; !ok {
			return Result{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: model response omitted %s", ErrUnavailable, key))
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
	payload := decisionPayload{TaskRelevant: &relevant, NeedsHuman: &needsHuman, Model: response.Model}
	// urgency is optional: a provider that does not answer score questions
	// still produces a usable assessment, and a malformed score it did claim to
	// answer is a bad response rather than a silent zero.
	if answer, ok := response.Answers["urgency"]; ok {
		if answer.Type != "score" || !finiteProbability(answer.Score) {
			return Result{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid urgency answer", ErrUnavailable))
		}
		payload.Urgency = answer.Score
	}
	if len(options) > 1 {
		choice := response.Answers["next_option"]
		if choice.Type != "choice" {
			return Result{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid next_option answer", ErrUnavailable))
		}
		payload.NextOption = choice.Choice
		payload.Confidence = choice.Confidence
		payload.Probabilities = choice.Probabilities
	}
	return validateDecision(payload, options, configuredModel)
}

// validateDecision is the one validator behind every protocol. It enforces the
// numeric bounds, the option vocabulary and the probability sum without
// caring which wire shape produced the values.
func validateDecision(payload decisionPayload, options []string, configuredModel string) (Result, error) {
	if payload.TaskRelevant == nil || !finite(*payload.TaskRelevant) {
		return Result{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid task_relevant answer", ErrUnavailable))
	}
	if payload.NeedsHuman == nil || !finite(*payload.NeedsHuman) {
		return Result{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid needs_human answer", ErrUnavailable))
	}
	assessment := attn.Assessment{
		NeedsHuman: payload.NeedsHuman, TaskRelevant: payload.TaskRelevant,
		Summary: attn.BoundSummary(payload.Summary),
	}
	if payload.Urgency != nil {
		if !finite(*payload.Urgency) {
			return Result{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid urgency answer", ErrUnavailable))
		}
		assessment.Urgency = payload.Urgency
	}
	if len(options) == 1 {
		assessment.NextOption = options[0]
		assessment.Probabilities = map[string]float64{options[0]: 1}
	} else {
		if !finiteProbability(payload.Confidence) {
			return Result{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid next_option confidence", ErrUnavailable))
		}
		if !contains(options, payload.NextOption) {
			return Result{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: model selected unauthorized option %q", ErrUnavailable, payload.NextOption))
		}
		probabilities, err := validateChoiceProbabilities(payload.Probabilities, options)
		if err != nil {
			return Result{}, err
		}
		assessment.NextOption = payload.NextOption
		assessment.Probabilities = probabilities
		assessment.Confidence = payload.Confidence
	}
	assessment.Model = strings.TrimSpace(payload.Model)
	if assessment.Model == "" {
		assessment.Model = configuredModel
	}
	return Result{Assessment: assessment, TaskRelevant: *payload.TaskRelevant, Model: assessment.Model}, nil
}

func validateNoul(answer apiAnswer, key string) (float64, error) {
	if answer.Type != "noul" || answer.Noul == nil || !finite(*answer.Noul) {
		return 0, WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid %s answer", ErrUnavailable, key))
	}
	return *answer.Noul, nil
}

func validateChoiceProbabilities(values map[string]float64, options []string) (map[string]float64, error) {
	if len(values) != len(options) {
		return nil, WithReason(ReasonBadResponse, fmt.Errorf("%w: choice probabilities do not cover authorized options", ErrUnavailable))
	}
	total := 0.0
	for _, option := range options {
		value, ok := values[option]
		if !ok || !finite(value) {
			return nil, WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid probability for %s", ErrUnavailable, option))
		}
		total += value
	}
	if math.Abs(total-1) > 0.01 {
		return nil, WithReason(ReasonBadResponse, fmt.Errorf("%w: choice probabilities sum to %.4f", ErrUnavailable, total))
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

func parseFreshUntil(a attn.Artifact) (time.Time, error) {
	if strings.TrimSpace(a.FreshUntil) == "" {
		return time.Time{}, errors.New("attention: artifact has no freshness deadline")
	}
	fresh, err := time.Parse(time.RFC3339Nano, a.FreshUntil)
	if err != nil {
		return time.Time{}, fmt.Errorf("attention: invalid freshness deadline: %w", err)
	}
	if !time.Now().UTC().Before(fresh) {
		return time.Time{}, attn.ErrStale
	}
	return fresh, nil
}

// Persist re-reads the artifact after the remote call, ensuring an assessment
// can only be attached to the same event and revision. It writes through the
// existing attention writer and therefore preserves atomic 0600 publication.
func Persist(path string, original attn.Artifact, result Result) error {
	if err := validateAssessment(&original, result.Assessment); err != nil {
		return err
	}
	if _, err := parseFreshUntil(original); err != nil {
		return err
	}
	return attn.CompareAndSwap(path, original.EventID, original.StateRevision, func(latest *attn.Artifact) error {
		if latest.WorktreeRoot != original.WorktreeRoot || latest.Branch != original.Branch {
			return attn.ErrStale
		}
		if _, err := parseFreshUntil(*latest); err != nil {
			return err
		}
		return attn.AttachAssessment(latest, result.Assessment)
	})
}

func validateAssessment(artifact *attn.Artifact, assessment attn.Assessment) error {
	if err := attn.AttachAssessment(artifact, assessment); err != nil {
		return err
	}
	return nil
}

var (
	secretPattern = regexp.MustCompile(`(?i)(bearer\s+|api[_-]?key|token|password|secret)\s*[:=]\s*[^\s,;]+`)
	// userinfoPattern catches credentials embedded in a connection string, the
	// shape a database driver puts in its own error line.
	userinfoPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]+(:[^/\s@]+)?@`)
	pathPattern     = regexp.MustCompile(`(?:/Users/[^\s"']+|/home/[^\s"']+|/private/tmp/[^\s"']+|/private/var/[^\s"']+|/tmp/[^\s"']+|/var/tmp/[^\s"']+|/var/folders/[^\s"']+|/opt/[^\s"']+|/srv/[^\s"']+|/root/[^\s"']+|/mnt/[^\s"']+|/Volumes/[^\s"']+|[A-Za-z]:\\Users\\[^\s"']+|[A-Za-z]:\\Program Files\\[^\s"']+)`)
	refPattern      = regexp.MustCompile(`^[A-Za-z0-9._\[\]:-]+$`)
)

// RedactLine is the one redaction a caller outside this package may apply: it
// collapses text to one line, removes secret-shaped and path-shaped runs, and
// bounds the result. A first-error line goes through exactly this before it is
// stored on a note or sent to a provider, so a log line cannot become a way to
// exfiltrate a token or a home directory.
func RedactLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	text = secretPattern.ReplaceAllString(text, "$1<redacted>")
	text = userinfoPattern.ReplaceAllString(text, "$1<redacted>@")
	text = pathPattern.ReplaceAllString(text, "<path>")
	const limit = 240
	runes := []rune(text)
	if len(runes) > limit {
		text = strings.TrimSpace(string(runes[:limit]))
	}
	return text
}

func redactText(value string, limit int) string {
	value = secretPattern.ReplaceAllString(value, "$1<redacted>")
	value = userinfoPattern.ReplaceAllString(value, "$1<redacted>@")
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
