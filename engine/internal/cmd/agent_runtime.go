package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/agentstream"
	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// The stream's explicit worktree and runtime metadata are the task boundary.
// No raw agent prompt, logs, source, environment, or credential is copied into
// the assessment. The same runner owns change delivery and consent checks.
func watchAgentRuntime(ctx context.Context, root string, scope agentstream.RuntimeTask, emit func(agentstream.RuntimeUpdate)) error {
	consent := attnResolvedConsent()
	client, err := consent.Client()
	if err != nil {
		emit(agentstream.RuntimeUpdate{Status: "not_attached", Reason: consent.ReasonOr(err)})
		return nil
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	task := model.TaskContext{Task: scope.Task, Services: scope.Services, RunIDs: scope.RunIDs, AuthorizedOptions: []string{"inspect_logs", "inspect_port", "inspect_dependency", "inspect_manifest", "continue_without_action", "ask_user_for_context"}}
	read := runner.WhileConfigured(attnResolvedConsent, client, runner.ReadStatus)
	scopedRead := func(ctx context.Context, binary, project string) (runner.Status, error) {
		s, err := read(ctx, binary, project)
		if err == nil && filepath.Clean(s.Scope.Root) != filepath.Clean(root) {
			return runner.Status{}, model.ErrStale
		}
		return s, err
	}
	trigger, closeChanges := attentionChanges(ctx, root)
	defer closeChanges()
	trigger.Check = runner.CheckConfiguration(attnResolvedConsent, client)
	id := runner.NewSessionID(root)
	home := runner.TelemetryHome()
	runner.AppendLifecycleTelemetry(home, root, "subscribed", id, nil)
	stats, err := runner.SubscribeWithStats(ctx, binary, root, 2*time.Second, client, task, scopedRead, id,
		func(out runner.Output, latency time.Duration) {
			runner.AppendSubscriptionTelemetry(home, root, task, out, latency, 2*time.Second)
		},
		func(value any) error {
			out, ok := value.(runner.Output)
			if !ok {
				return nil
			}
			update := agentstream.RuntimeUpdate{Status: out.Status, Reason: out.Reason}
			if out.Status == "clear" {
				update.Status = "observing"
				update.Reason = "no_eligible_event"
			}
			if out.Request != nil && (out.Status == "human_required" || out.Status == "unavailable") {
				// Deliberately omit artifact paths, evidence log paths and model prose.
				body, marshalErr := json.Marshal(struct {
					Status   string              `json:"status"`
					Revision string              `json:"revision"`
					Events   []attn.Event        `json:"events"`
					Options  []runner.OptionView `json:"options"`
					Decision *runner.HILDecision `json:"decision,omitempty"`
				}{out.Status, out.AttnRevision, out.Request.Events, out.Request.Options, out.Decision})
				if marshalErr != nil {
					return marshalErr
				}
				update.Handoff = string(body)
				update.Recheck = func(ctx context.Context) bool {
					fresh, err := scopedRead(ctx, binary, root)
					if err != nil {
						return false
					}
					artifact, err := runner.ArtifactFor(fresh)
					if err != nil || artifact.EventID != out.EventID || artifact.StateRevision != out.AttnRevision {
						return false
					}
					_, ok := attn.Select(artifact, scope.Services, scope.RunIDs)
					return ok
				}
			}
			emit(update)
			return nil
		}, trigger)
	event := "stopped"
	if err != nil {
		event = "error"
	}
	runner.AppendLifecycleTelemetry(home, root, event, id, &stats)
	return err
}

// prepareAgentRuntime projects the existing status facts, not another runtime
// registry. Only selected targets enter the native agent prompt. It neither
// adopts a manifest nor starts a missing/stopped service.
func prepareAgentRuntime(ctx context.Context, root string, scope agentstream.RuntimeTask) (string, error) {
	doc, err := buildStatusDocument(ctx, root, false, scope.Services)
	if err != nil {
		return "", err
	}
	if filepath.Clean(doc.Scope.Root) != filepath.Clean(root) {
		return "", fmt.Errorf("runtime worktree mismatch")
	}
	return agentRuntimeContext(doc, root, scope)
}

func agentRuntimeContext(doc statusDocument, root string, scope agentstream.RuntimeTask) (string, error) {
	if filepath.Clean(doc.Scope.Root) != filepath.Clean(root) {
		return "", fmt.Errorf("runtime worktree mismatch")
	}
	type target struct {
		Name    string `json:"name,omitempty"`
		RunID   string `json:"run_id,omitempty"`
		Running bool   `json:"running"`
		State   string `json:"state,omitempty"`
		Reason  string `json:"reason,omitempty"`
		Health  string `json:"health,omitempty"`
	}
	result := struct {
		Services []target `json:"services"`
		Runs     []target `json:"runs"`
	}{Services: []target{}, Runs: []target{}}
	for _, name := range scope.Services {
		found := false
		for _, s := range doc.Worktree.Services {
			if s.Name == name {
				row := target{Name: name, Running: s.Running}
				if s.HealthStatus != nil {
					row.Health = s.HealthStatus.Status
				}
				if s.RunID != nil {
					row.RunID = *s.RunID
				}
				if s.LastExit != nil {
					row.Reason = s.LastExit.Reason
				}
				result.Services = append(result.Services, row)
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("service not declared in selected worktree")
		}
	}
	for _, id := range scope.RunIDs {
		found := false
		for _, run := range append(append([]rpc.RunRecord(nil), doc.Runs...), doc.Exits...) {
			if run.ID == id {
				result.Runs = append(result.Runs, target{Name: run.Name, RunID: id, Running: run.Status == "running" || run.Status == "starting", State: run.Status, Reason: run.Reason})
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("run not recorded in selected worktree")
		}
	}
	data, err := json.Marshal(result)
	return string(data), err
}
