package cmd

import (
	"fmt"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/claudehook"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// attnLoopFacts is the project-scoped, read-only picture that tells a person
// whether Attention is only configured, or actually participating in this
// worktree. It never reads settings.local.json and never writes hook files.
type attnLoopFacts struct {
	inRepo        bool
	root          string
	hooks         claudehook.ProjectSettings
	delivery      claudehook.DeliveryView
	artifact      attn.Glance
	observedAfter bool
}

func collectAttnLoopFacts(project string, activity model.ActivityMetric) attnLoopFacts {
	var facts attnLoopFacts
	checkout, inRepo := groups.Locate(project)
	if !inRepo {
		return facts
	}
	facts.inRepo = true
	facts.root = checkout.Root
	if hooks, err := claudehook.InspectProjectSettings(facts.root); err == nil {
		facts.hooks = hooks
	}
	home := runner.TelemetryHome()
	if delivery, err := claudehook.SummarizeDelivery(home, model.WorktreeID(facts.root)); err == nil {
		facts.delivery = delivery
	}
	if artifact, err := attn.GlanceAt(home, facts.root); err == nil {
		facts.artifact = artifact
		facts.observedAfter = artifact.ObservedAfter(activity.LastObservedAt)
	}
	return facts
}

func applyAttnLoopJSON(state map[string]any, facts attnLoopFacts) {
	if !facts.inRepo {
		return
	}
	state["hooks"] = facts.hooks
	state["delivery"] = facts.delivery
	artifact := map[string]any{"present": facts.artifact.Present}
	if facts.artifact.Present {
		if facts.artifact.GeneratedAt != "" {
			artifact["generated_at"] = facts.artifact.GeneratedAt
		}
		if facts.artifact.EventKind != "" {
			artifact["event_kind"] = facts.artifact.EventKind
		}
		artifact["observed_after"] = facts.observedAfter
	}
	state["artifact"] = artifact
}

func writeAttnLoopText(b *strings.Builder, facts attnLoopFacts) {
	if !facts.inRepo {
		return
	}
	switch {
	case facts.hooks.Enabled:
		b.WriteString("  hooks     project .claude/settings.json enables oberth claude-hook\n")
	case facts.hooks.Present:
		b.WriteString("  hooks     project .claude/settings.json does not enable oberth claude-hook\n")
	default:
		b.WriteString("  hooks     this project has no committed .claude/settings.json enabling oberth claude-hook\n")
	}
	if facts.delivery.Sessions > 0 {
		line := fmt.Sprintf("  delivery  %d hook session(s); %d consumed, %d not consumed",
			facts.delivery.Sessions, facts.delivery.Consumed, facts.delivery.Unconsumed)
		if facts.delivery.LastReason != "" {
			line += " (last reason " + facts.delivery.LastReason + ")"
		}
		b.WriteString(line + "\n")
	}
	if facts.artifact.Present {
		line := "  artifact  published"
		if facts.artifact.EventKind != "" {
			line += " " + facts.artifact.EventKind
		}
		if facts.artifact.GeneratedAt != "" {
			line += " at " + facts.artifact.GeneratedAt
		}
		if facts.observedAfter {
			line += "; an observation followed"
		} else {
			line += "; no observation followed (not an automatic assessment)"
		}
		b.WriteString(line + "\n")
	}
}

func attnLoopNext(facts attnLoopFacts, consent model.Resolved, activity model.ActivityMetric) string {
	if !consent.Enabled || consent.Reason != "" {
		return model.ReasonHint(consent.Reason)
	}
	if facts.inRepo && !facts.hooks.Enabled {
		return "merge the JSON from `oberth agent claude-hook --project <worktree> --print-config` into the project's .claude/settings.json; this command does not write that file"
	}
	if facts.delivery.Unconsumed > 0 {
		return "hook findings were not delivered into the current agent turn; model calls alone do not mean Attention helped"
	}
	if facts.artifact.Present && !facts.observedAfter {
		return "a published anomaly has no following observation; this command does not start one"
	}
	if activity.State == "no_session" {
		return "attach a task-scoped `oberth attn assess --follow --project <worktree> --task <summary> --authorize <fixed-options>`; no background session was started"
	}
	return ""
}
