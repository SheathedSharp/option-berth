package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/spawn"
	"github.com/spf13/cobra"
)

// restartAdHocRun restarts one run by identity. It is the first-class form of
// "stop it and run that command again": the recorded argv is the command, so
// the engine never re-parses a display string or guesses where the quotes were.
// A run started before argv was recorded still gets the recorded start line.
func restartAdHocRun(cmd *cobra.Command, args []string) error {
	if len(restartOnly) > 0 {
		return usageError{fmt.Errorf("--run and --only select different subjects; use one")}
	}
	group := ""
	if len(args) == 1 {
		group = strings.TrimSpace(args[0])
	}
	if group == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolving the working directory: %w", err)
		}
		group, _ = projectAt(wd)
	}
	if group == "" {
		return failHint("not_found",
			"no project contains this run",
			"name the project: `oberth restart --run <id> <project>`")
	}

	c, err := connectForWrite(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()
	if !hasCapability(c.Hello(), rpc.CapabilityKillRuns) {
		return failHint("runtime_service_unavailable",
			"this runtime service does not know exact-run stops",
			"restart the runtime service with `oberth runtime service restart`, then retry")
	}

	var listed rpc.RunsListResult
	if err := c.Call(cmd.Context(), "runs.list", rpc.Empty{}, &listed); err != nil {
		return cliError(err)
	}
	records := append(append([]rpc.RunRecord{}, listed.Runs...), listed.Exited...)
	rec, err := selectNamedRun(records, group, restartRunFlag)
	if err != nil {
		return err
	}
	if len(rec.Argv) == 0 {
		return failHint("not_found",
			fmt.Sprintf("run %s predates recorded argv and cannot be restarted faithfully", rec.ID),
			"run the recorded start line instead: "+adHocStartCommand(rec))
	}
	if rec.Cwd == "" {
		return failHint("not_found",
			fmt.Sprintf("run %s has no recorded working directory", rec.ID),
			"run the recorded start line instead: "+adHocStartCommand(rec))
	}

	var snapshot []ports.ListeningPort
	if !restartJSON {
		snapshot, err = runtimeServiceList(cmd.Context(), c)
		if err != nil {
			return cliError(err)
		}
	}

	var stopped *rpc.KillEnvelope
	if rec.Status != "exited" {
		var envelope rpc.KillEnvelope
		stop := rpc.GroupsKillParams{
			Name:  group,
			Runs:  []string{rec.ID},
			Force: restartForce,
		}
		if err := c.Call(cmd.Context(), "groups.kill", stop, &envelope); err != nil {
			return cliError(err)
		}
		stopped = &envelope
		if !restartJSON {
			if err := reportKill(os.Stdout, envelope.Results, snapshot, false, false); err != nil {
				return err
			}
			fmt.Println()
		}
		if !envelope.OK {
			// A failed stop never starts a replacement: the same rule the
			// manifest restart holds.
			if restartJSON {
				if err := printJSON(runRestartDocument{RestartedOf: rec.ID, Stopped: stopped}); err != nil {
					return err
				}
			}
			return errSilent
		}
	}

	spawnGroup := rec.Group
	if spawnGroup == "" {
		spawnGroup = group
	}
	params := rpc.RunsSpawnParams{
		Argv:             append([]string(nil), rec.Argv...),
		Cwd:              rec.Cwd,
		Env:              callerEnv(),
		AllowOutsideHome: true,
	}
	if spawnGroup != "" {
		params.Group = &spawnGroup
	}
	if rec.Name != "" {
		name := rec.Name
		params.Name = &name
	}
	if rec.PortHint != nil {
		params.PortHint = rec.PortHint
	}
	var started rpc.RunsSpawnResult
	if err := c.Call(cmd.Context(), "runs.spawn", params, &started); err != nil {
		coded := cliError(err)
		if restartJSON {
			// The stop already happened; keep its receipt in the document
			// rather than losing it behind the stderr error.
			if printErr := printJSON(runRestartDocument{
				RestartedOf: rec.ID, Stopped: stopped, Error: err.Error(),
			}); printErr != nil {
				return printErr
			}
		}
		return coded
	}

	if restartJSON {
		return printJSON(runRestartDocument{
			RestartedOf: rec.ID,
			Stopped:     stopped,
			RunID:       started.RunID,
			PID:         started.PID,
			LogPath:     started.LogPath,
		})
	}
	printStarted(spawn.Resolution{Group: spawnGroup, Name: rec.Name}, started.PID, started.LogPath)
	if started.LogPath != "" {
		fmt.Println(display.Dim("logs " + started.LogPath))
	}
	return nil
}

// runRestartDocument is `restart --run --json`. The stop receipt is kept even
// when the replacement could not start; Error is present only then, and the
// standard stderr error still carries the code.
type runRestartDocument struct {
	RestartedOf string            `json:"restarted_of"`
	Stopped     *rpc.KillEnvelope `json:"stopped,omitempty"`
	RunID       string            `json:"run_id,omitempty"`
	PID         int               `json:"pid,omitempty"`
	LogPath     string            `json:"log_path,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// selectNamedRun picks one run from the group. An exact run id wins; a name is
// accepted only when it is unambiguous among the live runs, because a stale
// exited generation of the same name is not the process the caller is looking
// at.
func selectNamedRun(records []rpc.RunRecord, group, selector string) (rpc.RunRecord, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return rpc.RunRecord{}, usageError{fmt.Errorf("--run needs a run id or name")}
	}
	var byID []rpc.RunRecord
	var liveByName []rpc.RunRecord
	var allByName []rpc.RunRecord
	for _, rec := range records {
		if group != "" && rec.Group != group {
			continue
		}
		if rec.ID == selector {
			byID = append(byID, rec)
		}
		if rec.Name == selector {
			allByName = append(allByName, rec)
			if rec.Status != "exited" {
				liveByName = append(liveByName, rec)
			}
		}
	}
	if len(byID) > 0 {
		return newestRunRecord(byID), nil
	}
	if len(liveByName) > 1 {
		ids := make([]string, len(liveByName))
		for i, rec := range liveByName {
			ids[i] = rec.ID
		}
		sort.Strings(ids)
		return rpc.RunRecord{}, failHint("ambiguous",
			fmt.Sprintf("more than one live run is named %q in %s", selector, group),
			"use a run id: "+strings.Join(ids, ", "))
	}
	if len(liveByName) == 1 {
		return liveByName[0], nil
	}
	if len(allByName) > 0 {
		return newestRunRecord(allByName), nil
	}
	return rpc.RunRecord{}, failHint("not_found",
		fmt.Sprintf("no run %q in %s", selector, group),
		"run `oberth status --json --brief` to list the live and recent runs")
}

// newestRunRecord prefers a live generation, then the newest start. It is used
// only where the caller already accepted a name as the subject.
func newestRunRecord(records []rpc.RunRecord) rpc.RunRecord {
	sorted := append([]rpc.RunRecord(nil), records...)
	sort.SliceStable(sorted, func(i, j int) bool {
		leftLive := sorted[i].Status != "exited"
		rightLive := sorted[j].Status != "exited"
		if leftLive != rightLive {
			return leftLive
		}
		return runStartedAt(sorted[i]).After(runStartedAt(sorted[j]))
	})
	return sorted[0]
}
