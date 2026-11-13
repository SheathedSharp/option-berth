// Package codefacts answers "is this process running the code that is on disk
// now?" on demand, for a reader that already has the runtime group.
//
// The daemon's scan loop never calls this: it runs every few seconds and must
// not re-read source trees. Both `oberth status` and the daemon's groups.code
// read share this one implementation, so a client cannot get a different
// answer from the CLI and the app. A missing snapshot, an unreadable source,
// or a run from before this feature leaves the fields nil (unknown), never
// false.
package codefacts

import (
	"context"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/codeid"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// timeout bounds the optional source comparison. The caller must still answer
// when a source tree is huge or a filesystem is slow: the code fields then
// stay unknown, which is not the same as "unchanged".
const timeout = 10 * time.Second

// Annotate fills the per-service code identity verdicts on group. selected
// narrows the expensive read to those service names; nil checks every running
// service.
func Annotate(ctx context.Context, group *state.Group, selected map[string]bool) {
	if group == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
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
		if !svc.Running {
			continue
		}
		// The executable question is independent of the source snapshot: a
		// service may opt out of `sources:` and still run a built binary.
		annotateExecutable(svc)
		if svc.RunID == nil || *svc.RunID == "" {
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

// executableTolerance absorbs the published start time's second precision:
// started_at is RFC3339 without fractions, so a file built earlier in the same
// second must not read as newer than the run.
const executableTolerance = time.Second

// annotateExecutable answers the compiled-artifact question: is the program
// this run started still the file on disk? A directly-run binary rebuilt after
// the run began is the case `sources:` cannot see, because build output is
// deliberately excluded from the source fingerprint.
func annotateExecutable(svc *state.Service) {
	if svc.ExecutablePath == nil || *svc.ExecutablePath == "" || svc.StartedAt == nil {
		return
	}
	start, err := time.Parse(time.RFC3339, *svc.StartedAt)
	if err != nil {
		return
	}
	info, err := os.Stat(*svc.ExecutablePath)
	if err != nil {
		return
	}
	at := info.ModTime().UTC()
	modified := at.Format(time.RFC3339Nano)
	svc.ExecutableModifiedAt = &modified
	changed := at.After(start.Add(executableTolerance))
	svc.ExecutableChangedSinceStart = &changed
}
