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
// starting where that run started in a file shared with earlier runs.
func logsFromRun(ctx context.Context, c *client.Client, target, selector string) error {
	if _, err := strconv.Atoi(target); err == nil {
		return usageError{fmt.Errorf("--run selects a service run, not a port; name the service instead")}
	}
	rec, err := resolveRunForLogs(ctx, c, target, selector)
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
	meta.RunStartUnknown = runStartUnknown(rec.LogPath, *rec.LogOffset)

	if logsFirstErr {
		return logsFirstErrorFromFile(rec.LogPath, *rec.LogOffset, meta)
	}
	if logsFollow {
		return streamFileLogsFrom(ctx, rec.LogPath, *rec.LogOffset, logsJSONFlag, logsLinesFlag, meta)
	}
	doc, err := readFileLogsFrom(rec.LogPath, *rec.LogOffset, logsLinesFlag)
	if err != nil {
		return err
	}
	applyLogMetadata(&doc, meta)
	if logsJSONFlag {
		return printJSON(doc)
	}
	printLogHeader(doc.Source)
	for _, line := range doc.Lines {
		fmt.Println(line)
	}
	return nil
}

// resolveRunForLogs picks the run a --run selector names. `current` is the
// newest run of this service in this worktree; any other value is an exact run
// ID. The group filter is the same one `logs <service>` uses: a service name is
// scoped to the checkout the command runs in.
func resolveRunForLogs(ctx context.Context, c *client.Client, target, selector string) (rpc.RunRecord, error) {
	wd, err := os.Getwd()
	if err != nil {
		return rpc.RunRecord{}, fmt.Errorf("resolving the working directory: %w", err)
	}
	group, _ := projectAt(wd)
	if group == "" {
		return rpc.RunRecord{}, failHint("not_found",
			fmt.Sprintf("no project contains service %q", target),
			"run this command inside a worktree with an oberth.yaml")
	}

	var listed rpc.RunsListResult
	if err := c.Call(ctx, "runs.list", rpc.HostParams{Host: hostParams().Host}, &listed); err != nil {
		return rpc.RunRecord{}, cliError(err)
	}

	records := append(append([]rpc.RunRecord{}, listed.Runs...), listed.Exited...)
	return selectRunForLogs(records, group, target, selector)
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
	at, err := time.Parse(time.RFC3339, rec.StartedAt)
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

// readFileLogsFrom reads one run's lines: from offset, where that run began.
// A rotation (offset past the end) falls back to the top of the file and says
// so, because silently mixing older generations into the result would make the
// line numbers lie.
func readFileLogsFrom(path string, offset int64, lines int) (logsDocument, error) {
	all, _, unknown, err := readRunLines(path, offset)
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

// runStartUnknown reports a rotation: the recorded offset is past the current
// end, so the file no longer contains the run's start.
func runStartUnknown(path string, offset int64) bool {
	info, err := os.Stat(path)
	return err == nil && offset > info.Size()
}

// runTailStart is the byte where the last n lines of a run begin. `tail -c`
// starts there and follows, so a run-scoped follower shows this run's backlog
// and never a previous generation's tail.
func runTailStart(path string, offset int64, lines int) (int64, error) {
	all, starts, unknown, err := readRunLines(path, offset)
	if err != nil {
		return 0, err
	}
	if lines <= 0 {
		lines = 10
	}
	start := offset
	if unknown {
		start = 0
	}
	if len(all) <= lines {
		return start, nil
	}
	return start + starts[len(all)-lines], nil
}

// readRunLines returns the run's lines, each line's byte offset within the run,
// and whether the recorded start was no longer present in the file.
func readRunLines(path string, offset int64) ([]string, []int64, bool, error) {
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
	start := offset
	unknown := false
	if start < 0 || start > size {
		start = 0
		unknown = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, nil, unknown, err
	}
	// Read to EOF rather than to the size observed above: a service that is
	// still writing must not have its last lines cut off by a stale stat.
	data, err := io.ReadAll(f)
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
func logsFirstErrorFromFile(path string, offset int64, meta logMetadata) error {
	lines, _, unknown, err := readRunLines(path, offset)
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
