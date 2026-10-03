package cmd

import (
	"encoding/json"
	"os"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/ports"
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
	GroupID: commandGroupCore,
	Long: "Stop and start the project's services again. With --only, only the named\n" +
		"services are restarted; their port: auto claims stay attached to the\n" +
		"worktree so the service can reclaim the same addresses.",
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

type restartDocument struct {
	Stopped  rpc.KillEnvelope       `json:"stopped"`
	Services []rpc.GroupsStartChunk `json:"services"`
	StartID  string                 `json:"start_id,omitempty"`
	rpc.GroupsStartEnd
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

	var snapshot []ports.ListeningPort
	if !restartJSON {
		snapshot, err = hostSnapshot(cmd.Context(), c)
		if err != nil {
			return cliError(err)
		}
	}

	stop := rpc.GroupsKillParams{
		HostParams: hostParams(),
		Force:      restartForce,
		Release:    true,
		Only:       restartOnly,
	}
	if startParams.Name != nil {
		stop.Name = *startParams.Name
	} else if startParams.ConfigPath != nil {
		stop.ConfigPath = startParams.ConfigPath
	}
	var stopped rpc.KillEnvelope
	if err := c.Call(cmd.Context(), "groups.kill", stop, &stopped); err != nil {
		return cliError(err)
	}
	if !stopped.OK {
		if restartJSON {
			if err := printJSON(restartDocument{Stopped: stopped, Services: []rpc.GroupsStartChunk{}}); err != nil {
				return err
			}
			return errSilent
		}
		return reportKill(os.Stdout, stopped.Results, snapshot, false, false)
	}

	var initial rpc.GroupsStartResult
	stream, err := c.Stream(cmd.Context(), "groups.start", startParams, &initial)
	if err != nil {
		return cliError(err)
	}
	chunks, end, err := collectStart(stream)
	if err != nil {
		return err
	}

	if restartJSON {
		if err := printJSON(restartDocument{
			Stopped:        stopped,
			Services:       chunks,
			StartID:        initial.StartID,
			GroupsStartEnd: end,
		}); err != nil {
			return err
		}
		if len(end.Errors) > 0 {
			return errSilent
		}
		return nil
	}
	if err := reportKill(os.Stdout, stopped.Results, snapshot, false, false); err != nil {
		return err
	}
	for _, chunk := range chunks {
		printStartChunk(chunk)
	}
	printStartSummary(end)
	if len(end.Errors) > 0 {
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
