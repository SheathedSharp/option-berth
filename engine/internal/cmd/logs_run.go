package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
)

// firstErrorPattern is the fixed vocabulary --first-error locates. It is a
// match, not a diagnosis: the line may be noise, and the reader decides. The
// match is case-insensitive and not word-bounded, so "errors" and "Error:"
// both count.
var firstErrorPattern = regexp.MustCompile(`(?i)(error|exception|fatal|panic|traceback)`)

// logsFromRun is `logs <service> --run`: one generation of a service's output,
// the range that run owns in a file it shares with the generations around it.
func logsFromRun(ctx context.Context, c *client.Client, target, selector string) error {
	if _, err := strconv.Atoi(target); err == nil {
		return usageError{fmt.Errorf("--run selects a service run, not a port; name the service instead")}
	}
	rec, records, err := resolveRunForLogs(ctx, c, target, selector)
	if err != nil {
		return err
	}
	if rec.LogPath == "" {
		return failHint("not_found",
			fmt.Sprintf("run %s has no log file", rec.ID),
			fmt.Sprintf("the run did not go to a file; use `oberth logs %s` for its listener", rec.Name))
	}
	if rec.LogOffset == nil {
		return failHint("not_found",
			fmt.Sprintf("run %s has no log offset", rec.ID),
			"restart the service once with this version to record where its log starts")
	}
	meta := logMetadataFromRunRecord(rec)
	rng := runLogRange(rec, records)

	if logsFirstErr {
		return logsFirstErrorFromFile(rec.LogPath, rng, meta)
	}
	if logsFollow {
		return streamFileLogsFrom(ctx, rec.LogPath, rng, logsJSONFlag, logsLinesFlag, meta)
	}
	doc, err := readFileLogsFrom(rec.LogPath, rng, logsLinesFlag)
	if err != nil {
		return err
	}
	applyLogMetadata(&doc, meta)
	return printLogDocument(doc)
}

// logRange is the part of a log file one read covers, in bytes: [start, end).
// A whole-file read is a range that starts at zero. end < 0 means the range
// has no known end: the run that owns it is still writing, so the end of the
// file is its end for as long as the read lasts.
type logRange struct {
	start int64
	end   int64
}

// bounded says the range's last byte is already written: a finished run cannot
// grow another line, so following it is a read, not a wait.
func (r logRange) bounded() bool { return r.end >= 0 }

// runLogRange is where one run's bytes end in the file its service appends to
// across generations. The file is a single stream of bytes and only each run's
// start was recorded, so the end is where the next generation took over: a run
// that started after this one ended began writing exactly where this one
// stopped. Without such a run the range stays open, which is the truth for the
// newest run and for one that is still writing; closing it at the end of the
// file instead would report a later run's lines — and the error it died on —
// as this run's.
func runLogRange(rec rpc.RunRecord, records []rpc.RunRecord) logRange {
	rng := logRange{start: *rec.LogOffset, end: -1}
	if rec.Status != "exited" {
		return rng
	}
	endedAt := parseRunTime(rec.ExitedAt)
	if endedAt.IsZero() {
		// Without the moment this run ended, no later run can be shown to have
		// started after it; an open range is the honest answer.
		return rng
	}
	for _, other := range records {
		if other.ID == rec.ID || other.LogPath != rec.LogPath || other.LogOffset == nil {
			continue
		}
		startedAt := parseRunTime(other.StartedAt)
		if startedAt.IsZero() || startedAt.Before(endedAt) {
			// Overlapping this run: the two wrote interleaved bytes, so no byte
			// position separates them.
			continue
		}
		if rng.end < 0 || *other.LogOffset < rng.end {
			rng.end = *other.LogOffset
		}
	}
	return rng
}

// resolveRunForLogs picks the run a --run selector names and returns it with
// the run history it was picked from: the other generations are what bound its
// range. `current` is the newest run of this service in this worktree; any
// other value is an exact run ID. The group filter is the same one
// `logs <service>` uses: a service name is scoped to the checkout the command
// runs in.
func resolveRunForLogs(ctx context.Context, c *client.Client, target, selector string) (rpc.RunRecord, []rpc.RunRecord, error) {
	wd, err := os.Getwd()
	if err != nil {
		return rpc.RunRecord{}, nil, fmt.Errorf("resolving the working directory: %w", err)
	}
	group, _ := projectAt(wd)
	if group == "" {
		return rpc.RunRecord{}, nil, failHint("not_found",
			fmt.Sprintf("no project contains service %q", target),
			"run this command inside a worktree with an oberth.yaml")
	}

	var listed rpc.RunsListResult
	if err := c.Call(ctx, "runs.list", rpc.Empty{}, &listed); err != nil {
		return rpc.RunRecord{}, nil, cliError(err)
	}

	records := append(append([]rpc.RunRecord{}, listed.Runs...), listed.Exited...)
	rec, err := selectRunForLogs(records, group, target, selector)
	return rec, records, err
}

// selectRunForLogs is the pure half of resolveRunForLogs: one service in one
// group, newest first, with the run ID selector applied before ordering.
func selectRunForLogs(records []rpc.RunRecord, group, target, selector string) (rpc.RunRecord, error) {
	candidates := make([]rpc.RunRecord, 0, len(records))
	for _, rec := range records {
		if rec.Name != target || rec.Group != group {
			continue
		}
		if selector != "current" && rec.ID != selector {
			continue
		}
		candidates = append(candidates, rec)
	}
	if len(candidates) == 0 {
		if selector == "current" {
			return rpc.RunRecord{}, failHint("not_found",
				fmt.Sprintf("no recorded run for service %q in %s", target, group),
				"run `oberth up`, or `oberth status` to see the declared services")
		}
		return rpc.RunRecord{}, failHint("not_found",
			fmt.Sprintf("no run %q for service %q in %s", selector, target, group),
			"use `--run current`, or a run ID from `oberth status --json`")
	}

	// A live run is the service's current generation even when an overlapping
	// newer run has already failed: `current` must agree with the process the
	// rest of status calls running. Only among equally live (or equally
	// finished) runs does the newest start win.
	sort.SliceStable(candidates, func(i, j int) bool {
		leftLive := candidates[i].Status != "exited"
		rightLive := candidates[j].Status != "exited"
		if leftLive != rightLive {
			return leftLive
		}
		return runStartedAt(candidates[i]).After(runStartedAt(candidates[j]))
	})
	return candidates[0], nil
}

func runStartedAt(rec rpc.RunRecord) time.Time {
	return parseRunTime(rec.StartedAt)
}

// parseRunTime reads a timestamp off the wire; a record that carries none
// yields the zero time, which no comparison here treats as a real moment.
func parseRunTime(value string) time.Time {
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return at
}

func logMetadataFromRunRecord(rec rpc.RunRecord) logMetadata {
	return logMetadata{
		Service:   rec.Name,
		RunID:     rec.ID,
		PID:       rec.PID,
		Status:    rec.Status,
		ExitCode:  rec.ExitCode,
		Reason:    rec.Reason,
		LogOffset: rec.LogOffset,
	}
}

// readFileLogsFrom reads one run's lines: the range it owns inside the file it
// shares with the generations around it.
func readFileLogsFrom(path string, rng logRange, lines int) (logsDocument, error) {
	all, _, unknown, err := readRunLines(path, rng)
	if err != nil {
		return logsDocument{}, err
	}
	if lines <= 0 {
		lines = 10
	}
	truncated := len(all) > lines
	if truncated {
		all = all[len(all)-lines:]
	}
	if all == nil {
		all = []string{}
	}
	return logsDocument{
		Source:          path,
		Lines:           all,
		Truncated:       truncated,
		RunStartUnknown: unknown,
	}, nil
}

// runTailStart is the byte where the last n lines of a range begin. `tail -c`
// starts there and follows, so a run-scoped follower shows this run's backlog
// and never a previous generation's tail.
func runTailStart(path string, rng logRange, lines int) (int64, error) {
	all, starts, unknown, err := readRunLines(path, rng)
	if err != nil {
		return 0, err
	}
	if lines <= 0 {
		lines = 10
	}
	start := rng.start
	if unknown {
		start = 0
	}
	if len(all) <= lines {
		return start, nil
	}
	return start + starts[len(all)-lines], nil
}

// readRunLines returns the range's lines, each line's byte offset within the
// range, and whether the recorded range was no longer present in the file.
func readRunLines(path string, rng logRange) ([]string, []int64, bool, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil, false, failHint("not_found", "log file does not exist: "+path,
			"start the service with `oberth up`")
	}
	if err != nil {
		return nil, nil, false, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, nil, false, err
	}
	size := info.Size()
	start, end := rng.start, rng.end
	unknown := false
	// A rotation invalidates every recorded byte position: the start lies past
	// the end of the file, or the range ends behind its own start because a
	// later run found the file already truncated. What is left to read is the
	// whole file, said out loud — line numbers counted against it — because
	// guessing which part of it belongs to this run would be worse.
	if start < 0 || start > size || (end >= 0 && (end > size || end < start)) {
		start, end, unknown = 0, size, true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, nil, unknown, err
	}
	// Read to EOF rather than to the size observed above when the range has no
	// end: a service that is still writing must not have its last lines cut off
	// by a stale stat.
	var src io.Reader = f
	if end >= 0 {
		src = io.LimitReader(f, end-start)
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return nil, nil, unknown, err
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil, nil, unknown, nil
	}
	raw := strings.Split(text, "\n")
	lines := make([]string, len(raw))
	starts := make([]int64, len(raw))
	pos := int64(0)
	for i, line := range raw {
		starts[i] = pos
		pos += int64(len(line)) + 1
		lines[i] = strings.TrimRight(line, "\r")
	}
	return lines, starts, unknown, nil
}

// logsFirstErrorFromFile locates the first line in one run that matches the
// fixed vocabulary. It returns a location, not a cause, and does not summarise.
func logsFirstErrorFromFile(path string, rng logRange, meta logMetadata) error {
	lines, _, unknown, err := readRunLines(path, rng)
	if err != nil {
		return err
	}
	doc := logsDocument{
		Source:          path,
		Lines:           []string{},
		RunStartUnknown: unknown,
	}
	applyLogMetadata(&doc, meta)
	doc.RunStartUnknown = unknown
	if match := findFirstError(lines); match != nil {
		doc.FirstError = match
	} else {
		doc.NoFirstError = true
	}
	if logsJSONFlag {
		return printJSON(doc)
	}
	printLogHeader(path)
	if doc.FirstError == nil {
		fmt.Println(display.Dim("no error or exception line in this run"))
		return nil
	}
	fmt.Printf("%s %s\n", display.Dim(fmt.Sprintf("line %d", doc.FirstError.Line)), doc.FirstError.Text)
	return nil
}

func findFirstError(lines []string) *logMatch {
	for i, line := range lines {
		if firstErrorPattern.MatchString(line) {
			return &logMatch{Line: i + 1, Text: line}
		}
	}
	return nil
}
