package cmd

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/codeid"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// statusCodeTimeout bounds the optional source comparison. Status must still
// answer when a source tree is huge or a filesystem is slow: the code fields
// then stay unknown, which is not the same as "unchanged".
const statusCodeTimeout = 10 * time.Second

// annotateCodeFacts answers "is the process running the code that is on disk
// now?" for services this reader can check. The daemon's scanner never does
// this work: it runs every few seconds and must not re-read source trees. A
// missing snapshot, an unreadable source, or a run from before this feature
// leaves the fields null (unknown), never false.
func annotateCodeFacts(ctx context.Context, group *state.Group, selected map[string]bool) {
	if group == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, statusCodeTimeout)
	defer cancel()
	// Several services often share one source tree (they default to their own
	// cwd, which may be the same directory). Hash each tree once per read.
	type currentResult struct {
		snap codeid.Snapshot
		err  error
	}
	currentByTree := map[string]currentResult{}
	for i := range group.Services {
		svc := &group.Services[i]
		if selected != nil && !selected[svc.Name] {
			continue
		}
		if !svc.Running || svc.RunID == nil || *svc.RunID == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return
		}
		start, err := codeid.Load(*svc.RunID)
		if err != nil {
			continue
		}
		// The manifest may have changed where to look since this run started.
		// Comparing against the old definition would answer a different
		// question; a restart is what records a new baseline.
		currentSources, enabled := codeid.ResolveSources(svc.Sources)
		if !enabled || !slices.Equal(currentSources, start.Sources) {
			continue
		}
		if start.Head != "" {
			head := start.Head
			svc.CodeHead = &head
		}
		key := start.Root + "\x00" + strings.Join(start.Sources, "\x00")
		result, seen := currentByTree[key]
		if !seen {
			result.snap, result.err = codeid.Take(ctx, start.Root, start.Sources)
			currentByTree[key] = result
		}
		if result.err != nil {
			continue
		}
		current := result.snap
		changed, files := codeid.Compare(start, current)
		svc.CodeChangedSinceStart = &changed
		if !changed {
			continue
		}
		if len(files) > 0 {
			count := len(files)
			svc.CodeChangedFiles = &count
		}
		hint := "oberth restart --only " + svc.Name
		svc.CodeHint = &hint
	}
}
