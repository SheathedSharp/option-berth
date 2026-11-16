package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
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
		return restartWithoutManifest(cmd.Context(), startParams, err)
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

	// Read the code facts before the stop: a source change is why the restart
	// will run the same old build, and the warning is only actionable while the
	// caller can still cancel. It is best effort — an older host without
	// groups.code or an unreadable snapshot leaves the restart unchanged.
	warnings := restartCodeWarnings(cmd.Context(), c, startParams)
	if !restartJSON {
		printRestartWarnings(warnings)
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
			doc.Warnings = warnings
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
	records := append(append([]rpc.RunRecord{}, listed.Runs...), listed.Exited...)
	return adHocRestartRefusal(coded, records, params)
}

// restartWithoutManifest answers a restart whose project could not be resolved.
// The worktree has no usable manifest, but the run ledger still knows which run
// a name refers to, so a name it recorded gets exactly the start line the
// manifest-present refusal prints. Only when no requested name is in the ledger
// does the manifest refusal stand: it is the answer that tells the caller how to
// make the project usable.
func restartWithoutManifest(ctx context.Context, params rpc.GroupsStartParams, err error) error {
	var coded *cliErr
	if !errors.As(err, &coded) || coded.Code != "not_found" {
		return err
	}
	return adHocRestartRefusal(coded, runLedgerRecords(ctx), params)
}

// adHocRestartRefusal keeps a refusal's code and message and replaces its hint
// with one recorded start line per name the ledger knows. It is the only place
// that decides whether a refusal is explained this way, so a restart refusing an
// undeclared name and a restart with no manifest answer in the same words.
func adHocRestartRefusal(coded *cliErr, records []rpc.RunRecord, params rpc.GroupsStartParams) error {
	hint := adHocRestartHint(records, params.Only, restartGroup(params))
	if hint == "" {
		return coded
	}
	return &cliErr{Code: coded.Code, Msg: coded.Msg, Hint: hint}
}

// restartGroup is the project the request belongs to: the name the caller gave,
// or the project the working directory sits in. The ledger keeps runs from every
// worktree, so without it a name would match another project's run.
func restartGroup(params rpc.GroupsStartParams) string {
	if params.Name != nil {
		return *params.Name
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	group, _ := projectAt(wd)
	return group
}

// restartCodeWarnings reads the same code comparison status publishes, through
// the narrow groups.code call, for the services this restart is about to stop.
// It is best effort: an older runtime service without the method, or a failed
// read, leaves the restart exactly as it was — a warning is not worth blocking
// a stop the caller explicitly asked for.
func restartCodeWarnings(ctx context.Context, c *client.Client, params rpc.GroupsStartParams) []restartout.Warning {
	group := restartGroup(params)
	if group == "" {
		return nil
	}
	var result rpc.GroupsCodeResult
	if err := c.Call(ctx, "groups.code", rpc.GroupsCodeParams{Name: group}, &result); err != nil {
		return nil
	}
	return restartWarnings(result.Services, restartOnly)
}

// restartWarnings projects code facts onto the warnings a restart publishes. A
// null verdict means unknown and produces no warning: the command must not
// claim a change it did not observe.
func restartWarnings(codes []rpc.ServiceCode, only []string) []restartout.Warning {
	selected := make(map[string]bool, len(only))
	for _, name := range only {
		if name = strings.TrimSpace(name); name != "" {
			selected[name] = true
		}
	}
	out := make([]restartout.Warning, 0, len(codes))
	for _, code := range codes {
		if len(selected) > 0 && !selected[code.Name] {
			continue
		}
		codeChanged := code.Changed != nil && *code.Changed
		executableChanged := code.ExecutableChangedSinceStart != nil && *code.ExecutableChangedSinceStart
		if !codeChanged && !executableChanged {
			continue
		}
		warning := restartout.Warning{
			Service: code.Name, CodeChanged: codeChanged, ExecutableChanged: executableChanged,
		}
		if codeChanged && code.ChangedFiles != nil {
			files := *code.ChangedFiles
			warning.CodeChangedFiles = &files
		}
		out = append(out, warning)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// printRestartWarnings is the human half: what the stop is about to replace,
// printed before the stop so the caller can still cancel it.
func printRestartWarnings(warnings []restartout.Warning) {
	codeChanged := false
	for _, warning := range warnings {
		parts := make([]string, 0, 2)
		if warning.CodeChanged {
			codeChanged = true
			detail := "sources changed since it started"
			if warning.CodeChangedFiles != nil {
				detail += fmt.Sprintf(" (%d %s)", *warning.CodeChangedFiles,
					pluralWord(*warning.CodeChangedFiles, "file"))
			}
			parts = append(parts, detail)
		}
		if warning.ExecutableChanged {
			parts = append(parts, "executable was rebuilt after this run")
		}
		fmt.Printf("%s %s's %s\n", display.Yellow("warning:"), warning.Service, strings.Join(parts, " and "))
	}
	if codeChanged {
		fmt.Println(display.Dim("restart does not rebuild sources; build first if these changes need it"))
	}
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
