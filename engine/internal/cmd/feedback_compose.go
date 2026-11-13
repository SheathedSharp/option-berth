package cmd

// The interactive half of `oberth feedback report`: a person at a terminal
// picks what went wrong and which command from the same fixed vocabulary the
// flags accept, then writes the note. It chooses inputs only — Build still
// validates every value, the daily limits still apply, and the exact report is
// still shown before anything leaves the machine.

import (
	"os"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/feedback"
	"github.com/sheathedsharp/option-berth/engine/internal/terminal"
)

// feedbackComposeApplies is the whole gate for the composer: a person at a
// terminal, no JSON, no --yes (which means "send exactly what I passed"), and
// at least one of the two required fields missing. `--dry-run` still counts as
// composing, because nothing is sent either way.
func feedbackComposeApplies() bool {
	if feedbackReportJSON || feedbackYesFlag || !terminal.Interactive() {
		return false
	}
	return strings.TrimSpace(feedbackKindFlag) == "" || strings.TrimSpace(feedbackCommandFlag) == ""
}

// composeFeedback fills the two required fields and, when the person has not
// already stated one, the note. Values already given as flags are never asked
// again.
func composeFeedback() error {
	if strings.TrimSpace(feedbackKindFlag) == "" {
		options := feedback.KindOptions()
		choices := make([]terminal.Choice, 0, len(options))
		for _, option := range options {
			choices = append(choices, terminal.Choice{
				Value: string(option.Kind), Label: string(option.Kind), Detail: option.Short,
			})
		}
		picked, err := terminal.Select(os.Stderr, "What went wrong?", choices, 0)
		if err != nil {
			return err
		}
		feedbackKindFlag = choices[picked].Value
	}
	if strings.TrimSpace(feedbackCommandFlag) == "" {
		names := feedback.CommandNames()
		choices := make([]terminal.Choice, 0, len(names))
		for _, name := range names {
			choices = append(choices, terminal.Choice{Value: name, Label: name})
		}
		picked, err := terminal.Select(os.Stderr, "Which oberth command?", choices, 0)
		if err != nil {
			return err
		}
		feedbackCommandFlag = choices[picked].Value
	}
	if strings.TrimSpace(feedbackNoteFlag) == "" {
		note, err := terminal.Line(os.Stderr, "What did you see? (optional, shown before sending)", "")
		if err != nil {
			return err
		}
		feedbackNoteFlag = note
	}
	return nil
}
