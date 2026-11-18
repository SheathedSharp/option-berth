package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/claudehook"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/spf13/cobra"
)

func addClaudeHooks(parent *cobra.Command) {
	var project string
	var services, ids, authorized []string
	var lease time.Duration
	hook := &cobra.Command{Use: "claude-hook", Short: "Handle explicitly enabled project Claude lifecycle hooks; never installs hooks", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		// No hook event can cause Runtime autostart, updates or diagnostics on JSON stdout.
		in, err := claudehook.Decode(cmd.InOrStdin())
		if err != nil {
			return err
		}
		root, err := resolveWorktree(project)
		if project == "" || err != nil {
			return errors.New("claude-hook requires an explicit project worktree")
		}
		actual, err := resolveWorktree(in.Cwd)
		if err != nil || actual != root {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(claudehook.Response{Message: "Oberth Runtime: not_attached (worktree_mismatch)."})
		}
		in.Cwd = actual
		a := claudeAdapter(root)
		a.Task = model.TaskContext{Task: claudehook.Summary, Services: services, RunIDs: ids, AuthorizedOptions: authorized}
		a.Duration = lease
		a.EditOnly = os.Getenv("OBERTH_CLAUDE_TASK_MODE") == "edit"
		ctx, cancel := context.WithTimeout(cmd.Context(), 8*time.Second)
		defer cancel()
		out, err := a.Handle(ctx, in)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}}
	hook.Flags().StringVar(&project, "project", "", "Explicit selected worktree; must match native hook cwd")
	hook.Flags().StringSliceVar(&services, "service", nil, "Declared services explicitly allowed for this project's tasks")
	hook.Flags().StringSliceVar(&ids, "run", nil, "Exact registered ad-hoc run IDs; no adoption by name")
	hook.Flags().StringSliceVar(&authorized, "authorize", nil, "Explicit fixed read-only Attention option IDs")
	hook.Flags().DurationVar(&lease, "lease", claudehook.MaxLease, "Non-renewable observer lease, at most 10m")
	var printConfig bool
	hook.Flags().BoolVar(&printConfig, "print-config", false, "Print project hook configuration for review, without changing files")
	run := hook.RunE
	hook.RunE = func(cmd *cobra.Command, args []string) error {
		if !printConfig {
			return run(cmd, args)
		}
		root, err := resolveWorktree(project)
		if project == "" || err != nil {
			return errors.New("explicit project worktree required")
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		argv := []string{binary, "agent", "claude-hook", "--project", root, "--lease", lease.String()}
		if len(services) > 0 {
			argv = append(argv, "--service", strings.Join(services, ","))
		}
		if len(ids) > 0 {
			argv = append(argv, "--run", strings.Join(ids, ","))
		}
		if len(authorized) > 0 {
			argv = append(argv, "--authorize", strings.Join(authorized, ","))
		}
		for i, arg := range argv {
			argv[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
		}
		type entry struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		}
		type group struct {
			Hooks []entry `json:"hooks"`
		}
		hooks := map[string][]group{}
		for _, event := range []string{"UserPromptSubmit", "Stop", "StopFailure", "SessionEnd"} {
			hooks[event] = []group{{Hooks: []entry{{Type: "command", Command: strings.Join(argv, " "), Timeout: 10}}}}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"hooks": hooks})
	}
	parent.AddCommand(hook)
	var root, key, generation string
	worker := &cobra.Command{Use: "claude-observe", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return claudeAdapter(root).Run(ctx, key, generation)
	}}
	worker.Flags().StringVar(&root, "project", "", "Lease worktree")
	worker.Flags().StringVar(&key, "key", "", "Opaque local delivery key")
	worker.Flags().StringVar(&generation, "generation", "", "One task generation")
	parent.AddCommand(worker)
}

func claudeAdapter(root string) claudehook.Adapter {
	resolved := attnResolvedConsent()
	client, clientErr := resolved.Client()
	// Only an opaque configuration identity is kept in the short-lived cursor.
	hash := sha256.Sum256([]byte(client.Endpoint + "\x00" + client.Model + "\x00" + client.APIKey))
	identity := hex.EncodeToString(hash[:])
	a := claudehook.Adapter{Home: paths.Dir(), Root: root, Identity: identity}
	a.Consent = func() error {
		_, err := attnResolvedConsent().Client()
		if err != nil {
			return errors.New("attention_permission_or_configuration_required")
		}
		return nil
	}
	a.Prepare = func(ctx context.Context, task model.TaskContext) (string, error) {
		doc, err := claudeRuntimeStatus(ctx, root)
		if err != nil {
			return "", err
		}
		return claudeRuntimeContext(doc, root, task)
	}
	read := func(ctx context.Context, _, project string) (runner.Status, error) {
		if groups.Canonical(project) != root {
			return runner.Status{}, model.ErrStale
		}
		if err := runner.CheckConfiguration(attnResolvedConsent, client)(); err != nil {
			return runner.Status{}, err
		}
		doc, err := claudeRuntimeStatus(ctx, root)
		if err != nil {
			return runner.Status{}, err
		}
		s := runner.Status{AttnPath: doc.AttnPath, AttnRevision: doc.AttnRevision}
		s.Scope.Root = root
		return s, nil
	}
	a.Observe = func(ctx context.Context, task model.TaskContext, emit func(runner.Output) error) error {
		if clientErr != nil {
			return clientErr
		}
		trigger, closeChanges := attentionChanges(ctx, root)
		defer closeChanges()
		trigger.Check = runner.CheckConfiguration(attnResolvedConsent, client)
		id := runner.NewSessionID(root)
		home := runner.TelemetryHome()
		runner.AppendLifecycleTelemetry(home, root, "subscribed", id, nil)
		stats, err := runner.SubscribeWithStats(ctx, "", root, 2*time.Second, client, task, read, id, func(out runner.Output, latency time.Duration) {
			runner.AppendSubscriptionTelemetry(home, root, task, out, latency, 2*time.Second)
		}, func(value any) error {
			if out, ok := value.(runner.Output); ok {
				return emit(out)
			}
			return nil
		}, trigger)
		runner.AppendLifecycleTelemetry(home, root, "stopped", id, &stats)
		return err
	}
	a.Fresh = func(ctx context.Context, task model.TaskContext, f claudehook.Feedback) bool {
		s, err := read(ctx, "", root)
		if err != nil {
			return false
		}
		artifact, err := runner.ArtifactFor(s)
		if err != nil || artifact.EventID != f.EventID || artifact.StateRevision != f.Revision {
			return false
		}
		_, ok := attn.Select(artifact, task.Services, task.RunIDs)
		return ok
	}
	a.Spawn = func(key, generation string) error {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		child := exec.Command(binary, "agent", "claude-observe", "--project", root, "--key", key, "--generation", generation)
		child.Dir = root
		null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer null.Close()
		child.Stdin = null
		child.Stdout = null
		child.Stderr = null
		if err = detachClaudeObserver(child); err != nil {
			return err
		}
		if err = child.Start(); err != nil {
			return err
		}
		go func() { _ = child.Wait() }()
		return nil
	}
	return a
}

// Read only an already-running Runtime. No local scanner, code hashing, git
// diff, log read, configuration creation or service autostart is hidden in hooks.
func claudeRuntimeStatus(ctx context.Context, root string) (statusDocument, error) {
	if noRuntimeServiceFlag {
		return statusDocument{}, errors.New("runtime_service_required")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := dialRuntimeService(ctx)
	if err != nil {
		return statusDocument{}, errors.New("runtime_service_required")
	}
	defer c.Close()
	var snap state.Snapshot
	if err = c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
		return statusDocument{}, err
	}
	doc := statusDocument{}
	doc.Scope.Root = root
	doc.Observation = publicObservation(snap.Observation)
	if doc.Observation == nil {
		doc.Observation = publicObservation(c.Hello().Observation)
	}
	found := false
	for _, g := range snap.Groups {
		if g.RootDir != nil && groups.Canonical(*g.RootDir) == root {
			doc.Worktree = g
			doc.Scope.Name = g.Name
			found = true
			break
		}
	}
	if !found {
		return doc, errors.New("project_not_registered_in_runtime")
	}
	rows, err := worktreeRuns(ctx, c, doc.Scope.Name)
	if err != nil {
		return doc, err
	}
	// The same owning store supplies all durable exits, not the UI's latest five.
	exits, err := statusExitsAll(ctx, doc.Scope.Name)
	if err != nil {
		return doc, err
	}
	doc.attentionExits = exits
	doc.Exits = statusExitDisplay(exits)
	for _, r := range rows {
		if r.Status != "exited" {
			doc.Runs = append(doc.Runs, r)
		} else {
			doc.attentionExits = mergeExitRecords(doc.attentionExits, []rpc.RunRecord{r})
		}
	}
	for _, p := range snap.Ports {
		if p.Group != nil && *p.Group == doc.Scope.Name {
			doc.Ports = append(doc.Ports, p)
		}
	}
	publishAttention(&doc)
	return doc, nil
}
