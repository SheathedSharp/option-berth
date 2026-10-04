package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/internal/docker"
	"github.com/sheathedsharp/option-berth/internal/draft"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/paths"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/userenv"
	"github.com/spf13/cobra"
)

var (
	initDryRunFlag bool
	initJSONFlag   bool
)

const (
	agentAuto   = "auto"
	draftLatest = "latest"
)

// initNote is the last thing a freshly written file says: the list is the
// user's to write — or to have their own agent draft and then adopt
// (init draft / init adopt). Either way the write is a person's
// act: a drafting run only ever produces a draft under ~/.option-berth/drafts,
// and only init adopt turns one into this file.
const initNote = `
# What was listening inside this project when the file was written is in the
# comments above, and so is what the project's own files declare, next to the
# shape of an entry to copy. The list is yours — write it, or have your own
# agent draft it: oberth init draft.
`

var initCmd = &cobra.Command{
	Use:     "init",
	Short:   "Create the starter oberth.yaml",
	GroupID: commandGroupCore,
	Long: "Create a starter oberth.yaml for the current worktree. It records the\n" +
		"project identity and leaves the service list for a person to review.\n\n" +
		"Use `oberth init draft` when a local agent should prepare a draft, and\n" +
		"`oberth init adopt` when a person has chosen a draft to write.",
	Args: cobra.NoArgs,
	RunE: initRun,
}

// initDraftCmd and initAdoptCmd keep the two human-controlled steps beside the
// initializer without making init itself a bag of unrelated modes.
var initDraftCmd = &cobra.Command{
	Use:   "draft [agent]",
	Short: "Ask a local agent to draft oberth.yaml",
	Long: "Ask the configured local agent to prepare one reviewable manifest draft.\n" +
		"The draft is never written to the worktree until `oberth init adopt` is run.\n" +
		"If more than one supported agent is installed, name it as the argument.\n" +
		"With --progress, emit NDJSON progress events and finish with the draft result.",
	Args: cobra.MaximumNArgs(1),
	RunE: initDraftRun,
}

var initAdoptCmd = &cobra.Command{
	Use:   "adopt [draft]",
	Short: "Write a chosen manifest draft",
	Long: "Write the selected manifest draft into oberth.yaml. With no argument,\n" +
		"use the latest draft for this worktree. An existing manifest must be\n" +
		"explicitly replaced or merged.",
	Args: cobra.MaximumNArgs(1),
	RunE: initAdoptRun,
}

func init() {
	initCmd.Flags().BoolVar(&initDryRunFlag, "dry-run", false, "Print the proposed file instead of writing it")
	initCmd.Flags().BoolVar(&initJSONFlag, "json", false,
		"Output as JSON: what went in, plus the YAML itself when --dry-run")
	initDraftCmd.Flags().BoolVar(&initDraftDryRunFlag, "dry-run", false, "Print the draft without archiving it")
	initDraftCmd.Flags().BoolVar(&initDraftJSONFlag, "json", false, "Output as JSON")
	initDraftCmd.Flags().BoolVar(&initDraftProgressFlag, "progress", false, "Stream progress events as NDJSON")
	initAdoptCmd.Flags().BoolVar(&initAdoptReplaceFlag, "replace", false, "Replace an existing oberth.yaml")
	initAdoptCmd.Flags().BoolVar(&initAdoptMergeFlag, "merge", false, "Append services to an existing oberth.yaml")
	initAdoptCmd.Flags().BoolVar(&initAdoptDryRunFlag, "dry-run", false, "Preview the manifest without writing it")
	initAdoptCmd.Flags().BoolVar(&initAdoptDiffFlag, "diff", false, "Show the adoption diff (requires --dry-run)")
	initAdoptCmd.Flags().BoolVar(&initAdoptJSONFlag, "json", false, "Output as JSON")
	initCmd.AddCommand(initDraftCmd, initAdoptCmd)
	rootCmd.AddCommand(initCmd)
}

var (
	initDraftDryRunFlag   bool
	initDraftJSONFlag     bool
	initDraftProgressFlag bool
	initAdoptReplaceFlag  bool
	initAdoptMergeFlag    bool
	initAdoptDryRunFlag   bool
	initAdoptDiffFlag     bool
	initAdoptJSONFlag     bool
)

func initDraftRun(cmd *cobra.Command, args []string) error {
	var progress *draftProgress
	if initDraftProgressFlag {
		progress = newDraftProgress(os.Stdout)
		progress.emit("preparing", "reading project facts")
	}
	root, err := initRoot()
	if err != nil {
		if progress != nil {
			progress.emit("error", err.Error())
		}
		return err
	}
	_, cfg, err := proposeConfig(root)
	if err != nil {
		if progress != nil {
			progress.emit("error", err.Error())
		}
		return err
	}
	if progress != nil {
		progress.emit("starting", "project facts ready, starting the agent")
	}
	agent := agentAuto
	if len(args) == 1 {
		agent = args[0]
	}
	return initAgentRun(cmd, root, cfg, agent, initDraftDryRunFlag, initDraftJSONFlag, progress)
}

func initAdoptRun(cmd *cobra.Command, args []string) error {
	if initAdoptReplaceFlag && initAdoptMergeFlag {
		return usageError{fmt.Errorf("--replace and --merge cannot both be given")}
	}
	if initAdoptDiffFlag && !initAdoptDryRunFlag {
		return usageError{fmt.Errorf("--diff requires --dry-run")}
	}
	source := draftLatest
	if len(args) == 1 {
		source = args[0]
	}
	root, err := initRoot()
	if err != nil {
		return err
	}
	return initAdoptDraft(root, source, initAdoptReplaceFlag, initAdoptMergeFlag,
		initAdoptDryRunFlag, initAdoptDiffFlag, initAdoptJSONFlag)
}

func initRun(cmd *cobra.Command, args []string) error {
	root, err := initRoot()
	if err != nil {
		return err
	}
	_, cfg, err := proposeConfig(root)
	if err != nil {
		return err
	}
	target := groups.TargetIn(root)
	_, statErr := os.Lstat(target)
	exists := statErr == nil

	if exists && !initDryRunFlag {
		return fmt.Errorf("%s already exists; use `oberth init adopt` to update it", target)
	}

	data, err := groups.Marshal(cfg)
	if err != nil {
		return err
	}
	if _, err := groups.Parse(target, data); err != nil {
		return err
	}
	data = append(data, []byte(initNote)...)

	if initDryRunFlag {
		if initJSONFlag {
			return printJSON(initResult{
				Path:       target,
				Worktree:   cfg.Name,
				Services:   len(cfg.Services),
				Candidates: len(cfg.Candidates),
				YAML:       string(data),
			})
		}
		fmt.Print(string(data))
		return nil
	}
	if err := groups.WriteConfigFile(target, data); err != nil {
		return err
	}
	if initJSONFlag {
		return printJSON(initResult{
			Path:       target,
			Worktree:   cfg.Name,
			Services:   len(cfg.Services),
			Candidates: len(cfg.Candidates),
			Written:    true,
		})
	}
	fmt.Printf("wrote %s: worktree %s with %d %s\n",
		target, cfg.Name, len(cfg.Services), pluralWord(len(cfg.Services), "service"))
	if len(cfg.Services) == 0 {
		context := fmt.Sprintf("%d listening %s", len(cfg.Candidates), pluralWord(len(cfg.Candidates), "port"))
		if n := len(cfg.Declared); n > 0 {
			context += fmt.Sprintf(" and %d declared %s", n, pluralWord(n, "service"))
		}
		fmt.Fprintf(os.Stderr,
			"note: the list is yours to write — %s %s in the comments as context\n",
			context, wasWere(len(cfg.Candidates)+len(cfg.Declared)))
	}
	return nil
}

// wasWere keeps that note readable whether one thing was listening or several.
func wasWere(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func initRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, _, ok := groups.Find(cwd)
	if !ok {
		root = cwd
		fmt.Fprintf(os.Stderr, "note: %s is not inside a git repository; using it as the project root\n", cwd)
	}
	return root, nil
}

// resolveAgent picks the CLI to run: the flag's name, else agent.command,
// else the single supported CLI on PATH. The choice itself lives in
// internal/draft — the daemon resolves the same way through the same function —
// and this only maps its failures onto the CLI's own vocabulary: a name that is
// not supported is the caller's mistake (exit 2), everything else is a fact
// about this machine (not_found).
func resolveAgent(agent string) (draft.CLI, error) {
	want := strings.TrimSpace(agent)
	if want == agentAuto {
		want = ""
	}
	cli, err := draft.Resolve(want, loadedConfig.Agent.Command)
	switch {
	case err == nil:
		return cli, nil
	case errors.Is(err, draft.ErrUnknownAgent):
		return draft.CLI{}, usageError{err}
	case errors.Is(err, draft.ErrNotInstalled):
		return draft.CLI{}, failHint("not_found", err.Error(),
			"install it, or pick another agent by name")
	default:
		return draft.CLI{}, failHint("not_found", err.Error(),
			"pick another agent, or set agent.command in ~/.option-berth/config.yaml")
	}
}

// initAgentRun runs the drafting agent. It never writes the project's
// manifest: the draft is printed, archived under ~/.option-berth/drafts, and a
// person adopts it later.
func initAgentRun(cmd *cobra.Command, root string, cfg *groups.Config, agent string, dryRun, jsonOutput bool, progress *draftProgress) error {
	cli, err := resolveAgent(agent)
	if err != nil {
		// The desktop app starts this CLI with launchd's minimal PATH. Recover the
		// user's login-shell PATH before giving up on claude/codex, just as the
		// daemon does for project services. A terminal invocation that already
		// resolves an agent pays no shell-startup cost.
		if _, changed := userenv.Ensure(userenv.Timeout); changed {
			cli, err = resolveAgent(agent)
		}
	}
	if err != nil {
		if progress != nil {
			progress.emit("error", err.Error())
		}
		return err
	}
	if progress != nil {
		progress.emit("running", fmt.Sprintf("%s started, inspecting project files", cli.Name))
	}
	facts := draft.Facts{Root: root, Group: cfg.Name, CLI: cli.Name, Candidates: cfg.Candidates}
	if data, err := os.ReadFile(groups.TargetIn(root)); err == nil {
		facts.Existing = string(data)
	}
	prompt := draft.Brief(facts)

	request := draft.Request{
		CLI:     cli,
		Root:    root,
		Prompt:  prompt,
		Extra:   loadedConfig.Agent.Args,
		Model:   strings.TrimSpace(loadedConfig.Agent.Model),
		Timeout: loadedConfig.Agent.ResolvedTimeout(),
		Group:   cfg.Name,
		Name:    "draft",
		DryRun:  dryRun,
	}
	if progress != nil {
		request.Stream = progress
	}
	res, err := draft.Run(cmd.Context(), request)
	if err != nil {
		if progress != nil {
			progress.flush()
			progress.emit("error", draftRunError(err).Error())
		}
		return draftRunError(err)
	}
	if progress != nil {
		progress.flush()
		progress.emit("validating", "agent returned, validating and tidying the manifest")
	}

	if jsonOutput || progress != nil {
		result := initResult{
			Path: groups.TargetIn(root), Worktree: cfg.Name,
			Services: len(res.Config.Services), Candidates: len(cfg.Candidates),
			Agent: cli.Name, Draft: res.Path, YAML: res.Draft,
		}
		if progress != nil {
			result.Type = "done"
			return encodeJSONLine(os.Stdout, result)
		}
		return printJSON(initResult{
			Path: groups.TargetIn(root), Worktree: cfg.Name,
			Services: len(res.Config.Services), Candidates: len(cfg.Candidates),
			Agent: cli.Name, Draft: res.Path, YAML: res.Draft,
		})
	}
	if res.Path != "" {
		fmt.Printf("draft %s\n\n", res.Path)
	} else {
		fmt.Print("draft (not saved: --dry-run)\n\n")
	}
	fmt.Print(res.Draft)
	fmt.Print("\nadopt it with: oberth init adopt\n")
	return nil
}

// draftProgress turns the raw event stream of the configured local agent into
// small, stable NDJSON updates. The UI should show where the run is, not bind
// itself to Claude/Codex's private event vocabulary or expose long reasoning
// traces. Unknown frames still advance the activity clock with a generic state.
type draftProgress struct {
	mu      sync.Mutex
	out     io.Writer
	started time.Time
	pending string
	last    string
}

type draftProgressEvent struct {
	Type      string `json:"type"`
	Stage     string `json:"stage,omitempty"`
	Message   string `json:"message,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

func newDraftProgress(out io.Writer) *draftProgress {
	return &draftProgress{out: out, started: time.Now()}
}

func (p *draftProgress) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending += string(data)
	for {
		line, rest, ok := strings.Cut(p.pending, "\n")
		if !ok {
			break
		}
		p.pending = rest
		p.agentLine(strings.TrimSpace(line))
	}
	return len(data), nil
}

func (p *draftProgress) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if line := strings.TrimSpace(p.pending); line != "" {
		p.pending = ""
		p.agentLine(line)
	}
}

func (p *draftProgress) emit(stage, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emitLocked(stage, message)
}

func (p *draftProgress) emitLocked(stage, message string) {
	key := stage + "\x00" + message
	if key == p.last {
		return
	}
	p.last = key
	event := draftProgressEvent{
		Type: "progress", Stage: stage, Message: message,
		ElapsedMS: time.Since(p.started).Milliseconds(),
	}
	if stage == "error" {
		event.Type = "error"
		event.Stage = ""
	}
	// Progress is advisory. A closed GUI pipe must not turn a valid draft into
	// an agent failure, so the output error is intentionally ignored.
	_ = encodeJSONLine(p.out, event)
}

func (p *draftProgress) agentLine(line string) {
	if line == "" {
		return
	}
	var frame struct {
		Type string `json:"type"`
		Item struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Command string `json:"command"`
		} `json:"item"`
		Message struct {
			Content []struct {
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &frame) != nil {
		p.emitLocked("running", "agent is working")
		return
	}
	switch frame.Type {
	case "system":
		p.emitLocked("running", "agent connected, analyzing the project")
	case "thread.started", "turn.started":
		p.emitLocked("running", "agent is analyzing the project")
	case "item.started", "item.completed":
		if frame.Item.Command != "" {
			p.emitLocked("inspecting", "inspecting: "+shortProgress(frame.Item.Command))
		} else if frame.Item.Name != "" {
			p.emitLocked("inspecting", "using: "+frame.Item.Name)
		} else {
			p.emitLocked("running", "agent is tidying project info")
		}
	case "assistant":
		for _, content := range frame.Message.Content {
			if content.Name != "" {
				p.emitLocked("inspecting", "using: "+content.Name)
				return
			}
		}
		p.emitLocked("running", "agent is tidying project info")
	case "turn.completed", "result":
		p.emitLocked("validating", "agent returned, validating the manifest")
	default:
		p.emitLocked("running", "agent is working")
	}
}

func shortProgress(command string) string {
	command = strings.Join(strings.Fields(command), " ")
	if len(command) > 72 {
		return command[:69] + "..."
	}
	return command
}

// draftRunError maps a failed drafting run onto the CLI's codes: the agent's
// own non-zero exit is not this program's bug but it is the run's failure; a
// missing or unparsable draft is about the file it would have become; a
// timeout is a timeout; an interrupt stays an interrupt (exit 130).
func draftRunError(err error) error {
	var configErr *groups.ConfigError
	switch {
	case errors.Is(err, context.Canceled):
		return err
	case errors.Is(err, draft.ErrTimeout):
		return fail("timeout", "%s", err.Error())
	case errors.Is(err, draft.ErrNoDraft):
		return failHint("invalid_config", err.Error(),
			"the agent has to answer with the draft schema — its own output is above")
	case errors.As(err, &configErr):
		return fail("invalid_config", "%s", err.Error())
	case errors.Is(err, draft.ErrAgentFailed):
		return fail("internal", "%s", err.Error())
	default:
		return err
	}
}

// initAdoptDraft writes a draft into the project — the step option-berth refuses
// to take on its own behalf: a draft exists because a person asked an agent
// for one, and it becomes the manifest because a person says so. The bytes are
// written as they are, comments included: the draft's per-service notes are
// the file's provenance.
func initAdoptDraft(root, source string, replace, merge, dryRun, diff, jsonOutput bool) error {
	if source == draftLatest {
		_, worktree, _ := groups.Find(root)
		source = paths.Draft(groups.GroupName(root, worktree))
	}
	data, err := os.ReadFile(source)
	if err != nil {
		if os.IsNotExist(err) {
			return failHint("not_found", fmt.Sprintf("no draft to adopt at %s", source),
				"run `oberth init draft` first, or name one with `oberth init adopt <path>`")
		}
		return err
	}
	cfg, err := groups.Parse(source, data)
	if err != nil {
		return fail("invalid_config", "%s", err.Error())
	}

	target := groups.TargetIn(root)
	_, statErr := os.Lstat(target)
	exists := statErr == nil
	if exists && !replace && !merge && !dryRun {
		return fmt.Errorf("%s already exists; use `--replace` or `--merge`", target)
	}

	if merge && exists {
		adds := cfg.Services
		if len(adds) == 0 {
			return fmt.Errorf("nothing to merge into %s: the draft declares no services", target)
		}
		out, merged, err := groups.RenderServiceMerge(target, adds)
		if err != nil {
			return err
		}
		if dryRun {
			return reviewAdoption(target, source, merged.Name, out, len(merged.Services), len(adds), diff, jsonOutput)
		}
		if err := groups.WriteConfigFile(merged.Path, out); err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(initResult{
				Path: merged.Path, Worktree: merged.Name, Merged: len(adds),
				AdoptedFrom: source, Written: true,
			})
		}
		fmt.Printf("merged %d %s from %s into %s: worktree %s\n",
			len(adds), pluralWord(len(adds), "service"), source, target, merged.Name)
		return nil
	}

	if dryRun {
		return reviewAdoption(target, source, cfg.Name, data, len(cfg.Services), 0, diff, jsonOutput)
	}
	if err := groups.WriteConfigFile(target, data); err != nil {
		return err
	}
	if jsonOutput {
		return printJSON(initResult{
			Path: target, Worktree: cfg.Name, Services: len(cfg.Services),
			AdoptedFrom: source, Written: true,
		})
	}
	fmt.Printf("wrote %s: worktree %s with %d %s (adopted from %s)\n",
		target, cfg.Name, len(cfg.Services), pluralWord(len(cfg.Services), "service"), source)
	return nil
}

// proposeConfig is the file this run would write: what is listening and what the
// project's own files state, both as candidates written into comments.
//
// It hands back the scan it made as well, so a caller that wants to ask about
// the listeners does not have to scan the machine a second time — two scans
// milliseconds apart can disagree, and a file annotated from one while written
// from another would quietly describe a machine that never existed.
func proposeConfig(root string) ([]ports.ListeningPort, *groups.Config, error) {
	results, err := ports.Scan()
	if err != nil {
		return nil, nil, err
	}
	docker.EnrichPorts(results)
	ports.Enrich(results)

	_, index := groups.Attribute(results)
	cfg := groups.Propose(root, results, index)
	// What a compose file or a package.json declares goes on the config as
	// context, the same way the listeners do — not into the list. A service
	// declared in a file that is not this one is not this project's service:
	// `init` used to fold them in, and the argument for it ("those files state
	// a command outright") is exactly what `init draft` exists to weigh now.
	cfg.Declared = groups.Detect(root)
	return results, cfg, nil
}

// initResult is what `oberth init --json` reports on every path it can take:
// where the file is (or would be), what went into it, and — for --dry-run — the
// bytes themselves, so a caller can show them instead of re-rendering them.
type initResult struct {
	// Type is set to "done" only by `init draft --progress`; regular JSON
	// output remains the documented single result shape.
	Type       string `json:"type,omitempty"`
	Path       string `json:"path"`
	Worktree   string `json:"worktree,omitempty"`
	Services   int    `json:"services"`
	Candidates int    `json:"candidates"`
	Merged     int    `json:"merged,omitempty"`
	Written    bool   `json:"written"`
	YAML       string `json:"yaml,omitempty"`
	// Diff is the optional unified patch for an adoption preview. A requested
	// but empty patch is present as "" rather than being omitted.
	Diff *string `json:"diff,omitempty"`
	// Agent is the drafting CLI used.
	Agent string `json:"agent,omitempty"`
	// Draft is where the draft was archived ("" with --dry-run).
	Draft string `json:"draft,omitempty"`
	// AdoptedFrom is the draft file init adopt read.
	AdoptedFrom string `json:"adopted_from,omitempty"`
}

func pluralWord(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
