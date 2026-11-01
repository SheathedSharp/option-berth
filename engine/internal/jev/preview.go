package jev

// What `oberth jev preview` shows and what `oberth jev enable` prints afterwards.
// Both answer the same question in different tenses: "what exactly crosses the
// boundary?" A person enabling an optional remote assessment is entitled to a
// concrete answer, not a promise in a document.

import (
	"errors"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/attention"
)

// PreviewRequest returns the body the client would POST for this artifact and
// task, built by the same function the real call uses. It contacts nothing.
func PreviewRequest(artifact attention.Artifact, task TaskContext) (map[string]any, error) {
	options, err := task.validate(artifact.Options)
	if err != nil {
		return nil, err
	}
	// The preview has no configured model: name the placeholder the shape uses
	// so a reader is never told a specific model was chosen.
	body := jevRequest{
		State: safeState(artifact, task, options),
		Model: "<the configured model>",
		Questions: map[string]jevQuestion{
			"task_relevant": {Type: "noul"},
			"needs_human":   {Type: "noul"},
		},
	}
	if len(options) > 1 {
		body.Questions["next_option"] = jevQuestion{Type: "choice"}
	}
	encoded, err := encodePreview(body)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// encodePreview renders the request as the generic map a command prints, with
// field names preserved. Going through the typed struct keeps the preview and
// the real request from drifting apart.
func encodePreview(body jevRequest) (map[string]any, error) {
	state := map[string]any{
		"task":               body.State.Task,
		"worktree_id":        body.State.WorktreeID,
		"state_revision":     body.State.StateRevision,
		"event":              previewEvent(body.State.Event),
		"evidence_refs":      body.State.EvidenceRefs,
		"candidate_options":  body.State.CandidateOptions,
		"authorized_options": body.State.AuthorizedOptions,
		"model":              body.Model,
	}
	if body.State.Branch != "" {
		state["branch"] = body.State.Branch
	}
	if len(body.State.Events) > 1 {
		events := make([]map[string]any, 0, len(body.State.Events))
		for _, event := range body.State.Events {
			events = append(events, previewEvent(event))
		}
		state["events"] = events
	}
	questions := make([]map[string]string, 0, len(body.Questions))
	for name, question := range body.Questions {
		questions = append(questions, map[string]string{"name": name, "type": question.Type})
	}
	return map[string]any{"state": state, "questions": questions}, nil
}

func previewEvent(event jevEvent) map[string]any {
	out := map[string]any{"kind": event.Kind}
	for key, value := range map[string]any{
		"service": event.Service, "reason": event.Reason, "run_id": event.RunID,
	} {
		if value != "" {
			out[key] = value
		}
	}
	for key, value := range map[string]int{"port": event.Port, "count": event.Count} {
		if value != 0 {
			out[key] = value
		}
	}
	if event.DeclaredPort != nil {
		out["declared_port"] = *event.DeclaredPort
	}
	if event.ActualPort != nil {
		out["actual_port"] = *event.ActualPort
	}
	if event.ExitCode != nil {
		out["exit_code"] = *event.ExitCode
	}
	return out
}

// sendsAlways, sendsNever and sendsDepends describe the boundary in one place.
// Keeping them data rather than prose means `enable` and the docs cannot
// disagree without a test failing.
var (
	sendsAlways = []string{
		"a hashed worktree identity, never the path",
		"the length-limited branch label",
		"the event kind, service name, reason and exit code",
		"fixed evidence reference names such as status.worktree.services[api].last_exit",
		"the fixed option IDs offered by the event and the ones you authorized",
		"the task summary you write, length-limited and redacted",
		"the model name you configured",
	}
	sendsNever = []string{
		"service logs or log paths",
		"environment variables or file contents",
		"the API key",
		"absolute paths, home directories or unrelated worktrees",
		"shell commands or free-form model output",
	}
	sendsDepends = []string{
		"every anomaly currently in the worktree, as a bounded summary",
	}
)

// SendsSummary is the machine-readable form `enable --json` reports.
func SendsSummary() map[string]any {
	return map[string]any{
		"always":  append([]string(nil), sendsAlways...),
		"never":   append([]string(nil), sendsNever...),
		"depends": append([]string(nil), sendsDepends...),
	}
}

// HumanSendsSummary is the same thing as text, for a person reading the shell.
func HumanSendsSummary() string {
	var b strings.Builder
	b.WriteString("Will be sent:\n")
	writeBullets(&b, sendsAlways)
	b.WriteString("Will never be sent:\n")
	writeBullets(&b, sendsNever)
	b.WriteString("Depends on the event:\n")
	writeBullets(&b, sendsDepends)
	b.WriteString("\nRun `oberth jev preview` to see the exact body for the current event.")
	return b.String()
}

func writeBullets(b *strings.Builder, lines []string) {
	for _, line := range lines {
		b.WriteString("  - ")
		b.WriteString(line)
		b.WriteString("\n")
	}
}

// ErrPreviewNoOptions is returned when the artifact offers no option the caller
// authorized, which is a caller mistake rather than a provider failure.
var ErrPreviewNoOptions = errors.New("attention: no authorized option is offered by this event")
