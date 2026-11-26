package model

// World triage is Attention's native half: instead of one CLI read asking one
// question about one event, the runtime service reduces every worktree to a
// Runtime World and asks once per world revision. The payload is built here,
// from the world and nothing else, so what crosses the boundary is this file's
// decision rather than each caller's.
//
// What the payload never carries: service logs, log paths, environment
// variables, credentials, the worktree path, or an unredacted first-error line.
// A first-error line is the one piece of log text that may travel, and only
// after RedactLine has removed secret-shaped and path-shaped runs.

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
)

// WorldTriageResult is one validated triage of a worktree world. It is a
// recommendation about the notes in that world; it never executes anything.
type WorldTriageResult struct {
	TaskRelevant float64
	NeedsHuman   float64
	Urgency      *float64
	NextOption   string
	Confidence   *float64
	Summary      string
	Model        string
}

// worldPayload is the redacted causal state a provider sees. Field names are
// the contract: `worktree_id` is a hash, `branch` is a bounded label, and every
// other field is a name the world already published.
type worldPayload struct {
	WorktreeID string       `json:"worktree_id"`
	Branch     string       `json:"branch,omitempty"`
	Health     string       `json:"health"`
	Coverage   string       `json:"coverage"`
	Task       string       `json:"task,omitempty"`
	Services   []string     `json:"services,omitempty"`
	RootCauses []worldCause `json:"root_causes,omitempty"`
	Drifts     []worldDrift `json:"drifts,omitempty"`
	Loops      []worldLoop  `json:"loops,omitempty"`
}

type worldCause struct {
	Service    string           `json:"service"`
	Kind       string           `json:"kind"`
	Severity   string           `json:"severity"`
	Impacted   []string         `json:"impacted,omitempty"`
	PortHolder *worldPortHolder `json:"port_holder,omitempty"`
	FirstError string           `json:"first_error,omitempty"`
}

// worldPortHolder says what kind of owner holds a port. The pid, the command
// and the group name stay local: they identify a process on this machine, and
// the triage only needs to know whether this run is the holder.
type worldPortHolder struct {
	Kind string `json:"kind"`
}

type worldDrift struct {
	Service  string `json:"service"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
}

type worldLoop struct {
	Service string `json:"service"`
	Kind    string `json:"kind"`
	Count   int    `json:"count"`
}

type worldDecisionRequest struct {
	State     worldPayload           `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]apiQuestion `json:"questions"`
}

// WorldTriageOptions is the fixed option vocabulary this world offers. It is
// deterministic: a note that can only be read offers inspection, a note whose
// remediation is a local restart offers restart, and a foreign port holder
// offers asking the person instead.
func WorldTriageOptions(world runtimecore.World) []string {
	offered := []string{"inspect_logs", "continue_without_action"}
	if worldOffersRestart(world) {
		offered = append(offered, "restart_service")
	}
	if worldOffersHumanDecision(world) {
		offered = append(offered, "ask_user_for_context")
	}
	sort.Strings(offered)
	return offered
}

func worldOffersRestart(world runtimecore.World) bool {
	for _, root := range world.Diagnosis.RootCauses {
		if strings.HasPrefix(root.Command, "oberth restart") {
			return true
		}
	}
	for _, service := range world.Services {
		for _, drift := range service.Drifts {
			if drift.Kind == runtimecore.DriftCodeStale || drift.Kind == runtimecore.DriftBinaryStale ||
				drift.Kind == runtimecore.DriftManifestStale {
				return true
			}
		}
	}
	return false
}

func worldOffersHumanDecision(world runtimecore.World) bool {
	for _, root := range world.Diagnosis.RootCauses {
		switch root.Kind {
		case runtimecore.DriftPortOccupied, runtimecore.DriftUnownedListener:
			if !strings.HasPrefix(root.Command, "oberth restart") {
				return true
			}
		case runtimecore.DriftRepeatedCrash:
			return true
		}
	}
	return false
}

// worldAuthorizedOptions intersects the caller's explicit allow-list with what
// this world offers. A native caller that passes none gets the offered set:
// there is no agent to authorize, and the set is derived from the world, never
// from a model's answer.
func worldAuthorizedOptions(world runtimecore.World, task TaskContext) ([]string, error) {
	offered := WorldTriageOptions(world)
	if len(task.AuthorizedOptions) == 0 {
		return offered, nil
	}
	options := make([]attn.Option, 0, len(offered))
	for _, id := range offered {
		options = append(options, attn.Option{ID: id})
	}
	return FilterAuthorizedOptions(options, task.AuthorizedOptions)
}

// evaluateWorldPayload builds the redacted payload. includeErrorLine comes from
// the resolved configuration, so a person who turned error lines off keeps them
// off everywhere.
func evaluateWorldPayload(world runtimecore.World, includeErrorLine bool, task string) worldPayload {
	payload := worldPayload{
		WorktreeID: hashWorktree(world.Worktree.Root),
		Branch:     redactIdentifier(world.Worktree.Branch, maxServiceBytes),
		Health:     string(world.Health),
		Coverage:   world.Coverage,
		Task:       redactText(task, maxTaskBytes),
	}
	for _, service := range world.Services {
		payload.Services = append(payload.Services, service.Name)
	}
	for _, root := range world.Diagnosis.RootCauses {
		cause := worldCause{
			Service:  root.Service,
			Kind:     string(root.Kind),
			Severity: string(root.Severity),
			Impacted: append([]string(nil), root.Impacted...),
		}
		if kind := portHolderKind(world, root.Service); kind != "" {
			cause.PortHolder = &worldPortHolder{Kind: kind}
		}
		if includeErrorLine {
			cause.FirstError = RedactLine(root.FirstError)
		}
		payload.RootCauses = append(payload.RootCauses, cause)
	}
	for _, service := range world.Services {
		for _, drift := range service.Drifts {
			switch drift.Kind {
			case runtimecore.DriftCodeStale, runtimecore.DriftBinaryStale, runtimecore.DriftManifestStale:
				payload.Drifts = append(payload.Drifts, worldDrift{
					Service: service.Name, Kind: string(drift.Kind), Severity: string(drift.Severity),
				})
			}
		}
	}
	for _, loop := range world.Diagnosis.Loops {
		payload.Loops = append(payload.Loops, worldLoop{
			Service: loop.Service, Kind: string(loop.Kind), Count: loop.Count,
		})
	}
	return payload
}

// portHolderKind narrows the state holder to the one field a provider may see.
func portHolderKind(world runtimecore.World, service string) string {
	for i := range world.Services {
		if world.Services[i].Name == service && world.Services[i].PortHolder != nil {
			return string(world.Services[i].PortHolder.Kind)
		}
	}
	return ""
}

// worldQuestions is the decisions-protocol view of a world triage. The prompt
// protocols state the same thing in decisionSchemaPrompt, using the same
// option list.
func worldQuestions(options []string) map[string]apiQuestion {
	questions := map[string]apiQuestion{
		"task_relevant": {
			Type:         "noul",
			Instructions: "Does this worktree runtime state materially affect the development task?",
			Criteria: map[string]string{
				"true":  "A root cause, drift or loop can block or invalidate the task's next useful step.",
				"false": "The state is unrelated to the task or can safely be ignored while it proceeds.",
			},
		},
		"needs_human": {
			Type:         "noul",
			Instructions: "Must the agent ask the person before acting on this state?",
			Criteria: map[string]string{
				"true":  "The next step is a user tradeoff or a side effect the agent must not choose alone.",
				"false": "A documented, reversible, authorized inspection is enough for now.",
			},
		},
		"urgency": {
			Type:         "score",
			Instructions: "How soon does this state stop the task from proceeding usefully?",
			Criteria:     map[string]string{"0": "not soon", "1": "immediately"},
		},
	}
	if len(options) > 1 {
		criteria := make(map[string]string, len(options))
		for _, id := range options {
			criteria[id] = optionDescription(id)
		}
		questions["next_option"] = apiQuestion{
			Type:         "choice",
			Instructions: "Which authorized next step should be presented first?",
			Criteria:     criteria,
		}
	}
	return questions
}

// EvaluateWorld asks the configured endpoint one question about a whole
// worktree world and returns the validated triage. It never mutates a note: the
// caller attaches the result under a revision compare-and-swap, so a slow
// answer cannot describe a world that has moved on.
func (c Client) EvaluateWorld(ctx context.Context, world runtimecore.World, task TaskContext) (WorldTriageResult, error) {
	client, err := c.normalized()
	if err != nil {
		return WorldTriageResult{}, err
	}
	if strings.TrimSpace(world.Worktree.Root) == "" {
		return WorldTriageResult{}, errors.New("attention: world has no worktree root")
	}
	if strings.TrimSpace(world.Revision) == "" {
		return WorldTriageResult{}, errors.New("attention: world has no revision")
	}
	options, err := worldAuthorizedOptions(world, task)
	if err != nil {
		return WorldTriageResult{}, err
	}
	summary := strings.TrimSpace(task.Task)
	if summary == "" {
		summary = "Triage this worktree's runtime state and name the next useful step."
	}
	payload := evaluateWorldPayload(world, client.IncludeErrorLine, summary)
	body, err := encodeStructuredRequest(client.Protocol, client.Model, payload, worldQuestions(options),
		worldDecisionRequest{State: payload, Model: client.Model, Questions: worldQuestions(options)})
	if err != nil {
		return WorldTriageResult{}, err
	}
	responseBody, err := client.exchange(ctx, body, nil)
	if err != nil {
		return WorldTriageResult{}, err
	}
	result, err := decisionResult(client.Protocol, responseBody, options, client.Model)
	if err != nil {
		return WorldTriageResult{}, err
	}
	triage := WorldTriageResult{
		TaskRelevant: result.TaskRelevant,
		NextOption:   result.Assessment.NextOption,
		Summary:      result.Assessment.Summary,
		Model:        result.Model,
		Confidence:   result.Assessment.Confidence,
		Urgency:      result.Assessment.Urgency,
	}
	if result.Assessment.NeedsHuman != nil {
		triage.NeedsHuman = *result.Assessment.NeedsHuman
	}
	if strings.TrimSpace(triage.Model) == "" {
		triage.Model = client.Model
	}
	return triage, nil
}
