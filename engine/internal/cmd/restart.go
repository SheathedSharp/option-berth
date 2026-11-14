package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/restartout"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/spf13/cobra"
)

var (
	restartOnly             []string
	restartForce            bool
	restartJSON             bool
	restartAllowOutsideHome bool
)

var restartCmd = &cobra.Command{
	Use:     "restart [project]",
	Short:   "Restart a project's services",
	GroupID: commandGroupOperations,
	Long: "Stop and start the project's services again. With --only, only the named\n" +
		"services are restarted; their port: auto claims stay attached to the\n" +
		"worktree so the service can reclaim the same addresses. Only services the\n" +
		"manifest declares can be restarted: a run started with `oberth start` is\n" +
		"not a manifest service, so restart it by running that command again; the\n" +
		"refusal hint names the recorded command.",
	Args: cobra.MaximumNArgs(1),
	RunE: restartRun,
}

func init() {
	restartCmd.Flags().StringSliceVar(&restartOnly, "only", nil, "Restart only these services (comma separated)")
	restartCmd.Flags().BoolVarP(&restartForce, "force", "f", false, "Send SIGKILL instead of SIGTERM")
	restartCmd.Flags().BoolVar(&restartJSON, "json", false, "Output as JSON")
	restartCmd.Flags().BoolVar(&restartAllowOutsideHome, "allow-outside-home", false,
		"Allow services whose worktree is outside the user's home directory")
	rootCmd.AddCommand(restartCmd)
}

func restartRun(cmd *cobra.Command, args []string) error {
	startParams, cfg, err := projectStartParams(args, restartOnly, restartAllowOutsideHome)
	if err != nil {
		return err
	}

	c, err := connectForWrite(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()
	if err := requireConfigSupport(c, cfg); err != nil {
		return err
	}
	// Restarting triggers the same dependency waits a named start does, so a
	// named project gets the same manifest capability check.
	if err := requireNamedProjectSupport(cmd.Context(), c, startParams); err != nil {
		return err
	}

	var snapshot []ports.ListeningPort
	if !restartJSON {
		snapshot, err = runtimeServiceList(cmd.Context(), c)
		if err != nil {
			return cliError(err)
		}
	}

	stop := rpc.GroupsKillParams{
		Force:   restartForce,
		Release: true,
		Only:    restartOnly,
	}
	if startParams.Name != nil {
		stop.Name = *startParams.Name
	} else if startParams.ConfigPath != nil {
		stop.ConfigPath = startParams.ConfigPath
	}

	return restartout.Execute(cmd.Context(),
		func(ctx context.Context) (rpc.KillEnvelope, error) {
			var stopped rpc.KillEnvelope
			if err := c.Call(ctx, "groups.kill", stop, &stopped); err != nil {
				return stopped, restartRefusal(ctx, c, startParams, err)
			}
			return stopped, nil
		},
		func(ctx context.Context) (restartout.Start, error) {
			var result restartout.Start
			stream, err := c.Stream(ctx, "groups.start", startParams, &result.Initial)
			if err != nil {
				return result, cliError(err)
			}
			result.Chunks, result.End, err = collectStart(stream)
			return result, err
		},
		func(doc restartout.Document, complete bool) error {
			return reportRestart(doc, complete, snapshot)
		})
}

// reportRestart uses the normal CLI encoders and preserves the operation error
// for Execute/reportError to print on stderr with its original exit status.
func reportRestart(doc restartout.Document, complete bool, snapshot []ports.ListeningPort) error {
	if restartJSON {
		if err := printJSON(doc); err != nil {
			return err
		}
		if complete && (!doc.Stopped.OK || len(doc.Errors) > 0) {
			return errSilent
		}
		return nil
	}
	if err := reportKill(os.Stdout, doc.Stopped.Results, snapshot, false, false); err != nil {
		return err
	}
	if !doc.Stopped.OK {
		if complete {
			return errSilent
		}
		return nil
	}
	fmt.Println()
	for _, chunk := range doc.Services {
		printStartChunk(chunk)
	}
	if complete {
		printStartSummary(doc.GroupsStartEnd)
	}
	if complete && len(doc.Errors) > 0 {
		return errSilent
	}
	return nil
}

func collectStart(stream *client.Stream) ([]rpc.GroupsStartChunk, rpc.GroupsStartEnd, error) {
	defer stream.Close()
	var chunks []rpc.GroupsStartChunk
	for chunk := range stream.Chunks() {
		var item rpc.GroupsStartChunk
		if err := json.Unmarshal(chunk, &item); err != nil {
			continue
		}
		normalizeStartChunk(&item)
		// restart never waits, so a started service is never confirmed ready.
		markStartChunkUnconfirmed(&item)
		chunks = append(chunks, item)
	}
	end := <-stream.End()
	if end.Err != nil {
		return chunks, rpc.GroupsStartEnd{}, cliError(end.Err)
	}
	var summary rpc.GroupsStartEnd
	if err := end.Decode(&summary); err != nil {
		return chunks, rpc.GroupsStartEnd{}, err
	}
	return chunks, summary, nil
}

// restartRefusal replaces the runtime service's generic "not declared" hint with the
// equivalent start command when the name belongs to a run started with
// `oberth start`. Those runs have no manifest entry, so restart cannot rebuild
// them; the run ledger still carries the command, cwd and port hint the caller
// needs to do it themselves.
func restartRefusal(ctx context.Context, c *client.Client, params rpc.GroupsStartParams, err error) error {
	converted := cliError(err)
	coded, ok := converted.(*cliErr)
	if !ok || coded.Code != "not_found" || len(params.Only) == 0 {
		return converted
	}
	var listed rpc.RunsListResult
	if callErr := c.Call(ctx, "runs.list", rpc.Empty{}, &listed); callErr != nil {
		return converted
	}
	group := ""
	if params.Name != nil {
		group = *params.Name
	} else if wd, wdErr := os.Getwd(); wdErr == nil {
		group, _ = projectAt(wd)
	}
	records := append(append([]rpc.RunRecord{}, listed.Runs...), listed.Exited...)
	hint := adHocRestartHint(records, params.Only, group)
	if hint == "" {
		return converted
	}
	return &cliErr{Code: coded.Code, Msg: coded.Msg, Hint: hint}
}

// adHocRestartHint builds one start line per requested name that the ledger
// knows. A live run is named as such: starting it again would duplicate it.
func adHocRestartHint(records []rpc.RunRecord, only []string, group string) string {
	var hints []string
	for _, raw := range only {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		rec, ok := newestRunNamed(records, name, group)
		if !ok {
			continue
		}
		line := "to restart this ad-hoc run: " + adHocStartCommand(rec)
		if rec.Status != "exited" {
			line = "this ad-hoc run is still running; stop it first, then: " + adHocStartCommand(rec)
		}
		hints = append(hints, line)
	}
	return strings.Join(hints, "\n")
}

// newestRunNamed picks the newest record for a service, preferring the caller's
// group when it is known. The ledger keeps both live and exited runs, and the
// newest one is the command that was actually last used.
func newestRunNamed(records []rpc.RunRecord, name, group string) (rpc.RunRecord, bool) {
	var best rpc.RunRecord
	found := false
	for _, rec := range records {
		if rec.Name != name {
			continue
		}
		if group != "" && rec.Group != group {
			continue
		}
		if !found || runStartedAt(rec).After(runStartedAt(best)) {
			best = rec
			found = true
		}
	}
	return best, found
}

// adHocStartCommand is the recorded run as a start line. The command is the
// joined form the ledger keeps; the caller can edit it before running.
func adHocStartCommand(rec rpc.RunRecord) string {
	parts := []string{}
	if rec.Cwd != "" {
		parts = append(parts, "cd", rec.Cwd, "&&")
	}
	parts = append(parts, "oberth", "start", "-d", "--group", rec.Group, "--name", rec.Name)
	if rec.PortHint != nil && *rec.PortHint > 0 {
		parts = append(parts, "--port", strconv.Itoa(*rec.PortHint))
	}
	if rec.Cmd != "" {
		parts = append(parts, "--", rec.Cmd)
	}
	return strings.Join(parts, " ")
}
