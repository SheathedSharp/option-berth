package cmd

// `oberth attn notes` is the reader for Attention's native half. The runtime
// service tracks a note per causal finding in a worktree's Runtime World; this
// command shows them with the cause, the impacted services, the redacted first
// error and the exact next command, plus the optional System-1 triage when one
// is attached.
//
// It never writes: when nothing has been persisted yet it derives the same
// deterministic notes from a live world, because "no runtime service has looked
// yet" must not look like "nothing is wrong".

import (
	"context"
	"fmt"
	"strings"
	"time"

	attnengine "github.com/sheathedsharp/option-berth/engine/internal/attn/engine"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
	"github.com/spf13/cobra"
)

var attnNotesAllFlag bool

var attnNotesCmd = &cobra.Command{
	Use:   "notes",
	Short: "Tracked attention notes for a worktree: cause, first error, next command",
	Long: "Read the native Attention notes for one worktree. A note is kept for every\n" +
		"root cause, every non-fatal drift and every loop signal the Runtime World\n" +
		"reports, and it is resolved automatically once the world stops producing\n" +
		"it. Each note names the exact `oberth` command that addresses it and whether\n" +
		"that command only reads, restarts a local service, or needs a person.\n\n" +
		"With --all, recently resolved notes are shown as well, so a reader can see\n" +
		"what changed since the last look. Without a running Runtime Service the\n" +
		"notes are derived live from the same facts `oberth status` reads.",
	Args: cobra.NoArgs,
	RunE: runAttnNotes,
}

func init() {
	attnNotesCmd.Flags().BoolVar(&attnNotesAllFlag, "all", false,
		"Also show recently resolved notes")
}

func runAttnNotes(cmd *cobra.Command, _ []string) error {
	root, err := resolveWorktree(attnProjectFlag)
	if err != nil {
		return err
	}
	doc, source, err := attnNotesFor(cmd.Context(), root)
	if err != nil {
		return err
	}
	active := attnengine.ActiveNotes(doc)
	resolved := attnengine.ResolvedNotes(doc)

	document := map[string]any{
		"status":         "notes",
		"source":         source,
		"worktree":       root,
		"group":          doc.Group,
		"world_revision": doc.WorldRevision,
		"health":         doc.WorldHealth,
		"active":         active,
	}
	if attnNotesAllFlag {
		document["resolved"] = resolved
	}
	return attnPrint(document, func() string { return attnNotesText(root, doc, source, active, resolved) })
}

// attnNotesFor reads the persisted notes for a worktree, or derives the same
// deterministic notes from a live world when the runtime service has not
// persisted any yet. source says which of the two answered.
func attnNotesFor(ctx context.Context, root string) (attnengine.WorktreeNotes, string, error) {
	store := attnengine.NoteStore{Home: paths.Dir()}
	doc, err := store.Load(root)
	if err != nil {
		return attnengine.WorktreeNotes{}, "", err
	}
	if len(doc.Notes) > 0 || doc.WorldRevision != "" {
		return doc, "persisted", nil
	}
	world, err := runtimeInspectWorld(ctx, root)
	if err != nil {
		return attnengine.WorktreeNotes{}, "", err
	}
	derived, _ := attnengine.DeriveNotes(world, attnengine.WorktreeNotes{WorktreeRoot: root}, time.Now())
	return derived, "derived", nil
}

// attnNoteSummary is the small document `oberth attn status` carries. It reads
// only what is already persisted: a status read stays a status read, and
// deriving a world is the explicit `attn notes` command's job.
func attnNoteSummary(project string) map[string]any {
	checkout, ok := groups.Locate(project)
	if !ok {
		return nil
	}
	store := attnengine.NoteStore{Home: paths.Dir()}
	doc, err := store.Load(checkout.Root)
	if err != nil || (len(doc.Notes) == 0 && doc.WorldRevision == "") {
		return nil
	}
	return map[string]any{
		"active":         len(attnengine.ActiveNotes(doc)),
		"resolved":       len(attnengine.ResolvedNotes(doc)),
		"health":         string(doc.WorldHealth),
		"world_revision": doc.WorldRevision,
	}
}

// attnNotesSummaryLine is the one line `oberth attn status` prints for the
// notes of the current worktree.
func attnNotesSummaryLine(notes map[string]any) string {
	active, _ := notes["active"].(int)
	resolved, _ := notes["resolved"].(int)
	health, _ := notes["health"].(string)
	line := fmt.Sprintf("%d active, %d resolved", active, resolved)
	if health != "" {
		line += " · world " + health
	}
	return line + " · `oberth attn notes`"
}

func attnNotesText(root string, doc attnengine.WorktreeNotes, source string, active, resolved []attnengine.Note) string {
	var b strings.Builder
	head := "Attention notes: " + display.Bold(firstNonEmptyString(doc.Group, root))
	health := string(doc.WorldHealth)
	if health == "" {
		health = "unknown"
	}
	head += "  " + display.Dim(health)
	if doc.WorldRevision != "" {
		head += "  " + display.Dim("revision "+doc.WorldRevision)
	}
	if source == "derived" {
		head += "  " + display.Dim("(derived live; no runtime service has persisted these yet)")
	}
	b.WriteString(head + "\n")

	if len(active) == 0 {
		b.WriteString("  no open notes\n")
	}
	for _, note := range active {
		b.WriteString(attnNoteText(note, "  "))
	}
	shown := resolved
	if !attnNotesAllFlag {
		shown = nil
	}
	if len(shown) > 0 {
		fmt.Fprintf(&b, "  %s\n", display.Dim(fmt.Sprintf("resolved (%d, most recent first)", len(shown))))
		for _, note := range shown {
			b.WriteString(attnNoteText(note, "  "))
		}
	} else if len(resolved) > 0 && !attnNotesAllFlag {
		fmt.Fprintf(&b, "  %s\n", display.Dim(fmt.Sprintf("%d resolved note(s); --all shows them", len(resolved))))
	}
	return strings.TrimRight(b.String(), "\n")
}

func attnNoteText(note attnengine.Note, indent string) string {
	var b strings.Builder
	status := string(note.Status)
	if note.Status == attnengine.NoteResolved {
		status = "resolved " + note.ResolvedAt
	}
	head := fmt.Sprintf("%s%s %s · %s · %s", indent, attnSeverityText(note.Severity), firstNonEmptyString(note.Service, "worktree"),
		attnengine.KindLabel(note), note.Summary)
	if note.Status != attnengine.NoteOpen && note.Status != attnengine.NoteTriaged {
		head += " " + display.Dim("["+status+"]")
	}
	b.WriteString(head + "\n")
	if len(note.Impacted) > 0 {
		fmt.Fprintf(&b, "%s  impacted: %s\n", indent, strings.Join(note.Impacted, ", "))
	}
	if note.FirstError != "" {
		fmt.Fprintf(&b, "%s  first error: %s\n", indent, note.FirstError)
	}
	if note.Command != "" {
		fmt.Fprintf(&b, "%s  next: %s (%s)\n", indent, note.Command, firstNonEmptyString(note.Safety, "read_only"))
	}
	if line := attnTriageLine(note.Triage); line != "" {
		fmt.Fprintf(&b, "%s  triage: %s\n", indent, line)
	}
	return b.String()
}

// attnSeverityText is the short colour-coded rank a reader scans first.
func attnSeverityText(severity runtimecore.Severity) string {
	label := "[" + string(severity) + "]"
	switch severity {
	case runtimecore.SeverityCritical:
		return display.Red(label)
	case runtimecore.SeverityHigh:
		return display.Yellow(label)
	default:
		return display.Dim(label)
	}
}

func attnTriageLine(triage *attnengine.TriageResult) string {
	if triage == nil {
		return ""
	}
	if triage.UnavailableReason != "" {
		return "unavailable (" + triage.UnavailableReason + ")"
	}
	var parts []string
	if triage.NeedsHuman != nil {
		parts = append(parts, fmt.Sprintf("needs_human %.2f", *triage.NeedsHuman))
	}
	if triage.Urgency != nil {
		parts = append(parts, fmt.Sprintf("urgency %.2f", *triage.Urgency))
	}
	if triage.NextOption != "" {
		parts = append(parts, triage.NextOption)
	}
	if triage.Summary != "" {
		parts = append(parts, `"`+triage.Summary+`"`)
	}
	if triage.AutoReconcileEligible {
		parts = append(parts, "reconcile-eligible")
	}
	source := strings.TrimSpace(strings.Trim(triage.Mode+" / "+triage.Model, " /"))
	if source != "" {
		parts = append(parts, "("+source+")")
	}
	return strings.Join(parts, " · ")
}
