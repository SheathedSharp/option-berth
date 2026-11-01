package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attention"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/draft"
	"github.com/sheathedsharp/option-berth/engine/internal/git"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"github.com/spf13/cobra"
)

// statusExitsShown caps how many finished runs the aggregate carries. The
// question it answers is "why did this last fail", which is about the last one
// or two — the whole registry is a different command's business.
const statusExitsShown = 5

// statusFilesShown caps the changed-file rows a full status carries. The list
// is unbounded in git, and a dirty monorepo can make one status read larger
// than the rest of the document combined. `oberth git files` still returns all
// of them, and the document says how many were left out.
const statusFilesShown = 100

var (
	statusJSONFlag   bool
	statusNoMarkFlag bool
	statusBriefFlag  bool
	statusOnlyFlag   []string
)

var statusCmd = &cobra.Command{
	Use:     "status [path]",
	Short:   "This project right now, and what changed since you last looked",
	GroupID: commandGroupFacts,
	Long: "One read for the start of a work session: what the manifest declares, what is\n" +
		"actually running, which listeners belong to it, what the repository looks\n" +
		"like, whether a drafted manifest is waiting to be adopted — and, from the second\n" +
		"run on, what changed since the last one.\n\n" +
		"The document joins the manifest, its runtime evidence, and the repository summary\n" +
		"in one worktree-scoped answer. `ports[]` are the listeners attributed to this\n" +
		"worktree; detail stays where it was: `git diff` for lines, `logs` for output.\n" +
		"It reports and does not judge: the change list is facts, with no verdict about\n" +
		"whether any of it is good.\n\n" +
		"This command writes its own mark, in " + shortPath(paths.SeenCLI()) + " —\n" +
		"that is what the next run compares against. `oberth status --no-mark` looks\n" +
		"without moving it. The mark is this command's own file: the app keeps one of its\n" +
		"own, because a window that re-reads every few seconds must not move an anchor an\n" +
		"agent set before starting work. When a deterministic service or dependency anomaly exists, status\n" +
		"also publishes a derived agent attention artifact under BERTH_HOME; it never calls a model\n" +
		"or executes an action for that artifact.\n\n" +
		"With --json --brief the answer is the runtime facts only — services, ports, health,\n" +
		"the current exit reason and the next step — without the manifest copies, drafts,\n" +
		"git summary or change ledger. It is the cheapest read for an agent that already\n" +
		"knows which service it is working on: narrow it further with --only <service>.",
	Args: cobra.MaximumNArgs(1),
	RunE: statusRun,
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSONFlag, "json", false, "Output as JSON")
	statusCmd.Flags().BoolVar(&statusNoMarkFlag, "no-mark", false,
		"Show the change without moving the mark this command compares against")
	statusCmd.Flags().StringSliceVar(&statusOnlyFlag, "only", nil,
		"Only these services and their dependencies (comma separated)")
	statusCmd.Flags().BoolVar(&statusBriefFlag, "brief", false,
		"Keep only runtime facts: status, ports, health, exit reason and next step")
	rootCmd.AddCommand(statusCmd)
}

// statusDocument is what `status --json` prints. Every object in it belongs to
// another command too, so a reader learns one shape per kind of thing.
type statusDocument struct {
	Scope    display.Scope   `json:"scope"`
	Worktree state.Group     `json:"worktree"`
	Ports    []state.Port    `json:"ports"`
	Git      *git.Tree       `json:"git,omitempty"`
	Drafts   []draft.Pending `json:"drafts"`
	Exits    []rpc.RunRecord `json:"exits"`
	// AttentionPath points to a derived, fact-only handoff for an optional
	// agent adapter. It is absent when there is no current anomaly.
	AttentionPath string `json:"attention_path,omitempty"`
	// AttentionRevision lets an agent compare an artifact it read with the
	// exact deterministic facts represented by this status response.
	AttentionRevision string `json:"attention_revision,omitempty"`
	// Changed is absent on the first run: there is nothing to compare against,
	// which is not the same as nothing having changed (`empty` inside says
	// that).
	Changed *statusChange `json:"changed,omitempty"`
	// Observation is present when the listener table could not be trusted.
	// Ports and port_actual are then unknown, not empty; the manifest and run
	// records still describe what was declared and started.
	Observation *rpc.ObservationStatus `json:"observation,omitempty"`
}

func statusRun(cmd *cobra.Command, args []string) error {
	dir, err := statusDir(args)
	if err != nil {
		return err
	}
	doc, err := buildStatusDocument(cmd.Context(), dir, !statusNoMarkFlag, statusOnlyFlag)
	if err != nil {
		return err
	}
	if err := scopeStatusDocument(&doc, statusOnlyFlag); err != nil {
		return err
	}
	if statusBriefFlag {
		if statusJSONFlag {
			return printJSON(briefStatusFrom(doc))
		}
		printStatusBrief(doc)
		return nil
	}
	if statusJSONFlag {
		return printJSON(doc)
	}
	printStatus(doc)
	return nil
}

// statusDir is the directory a status read is about: the argument, or the
// working directory when there is none.
func statusDir(args []string) (string, error) {
	if len(args) == 1 {
		dir := strings.TrimSpace(args[0])
		if dir == "" {
			return "", usageError{fmt.Errorf("the path is empty")}
		}
		return dir, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolving the working directory: %w", err)
	}
	return wd, nil
}

// statusProject names the project a directory sits in. Saying which directory
// was looked at is the difference between "you are in the wrong place" and
// "this project has no manifest yet".
func statusProject(dir string) (name, root string, err error) {
	if n, r := projectAt(dir); n != "" {
		return n, r, nil
	}
	return "", "", failHint("not_found",
		fmt.Sprintf("no project at or above %s: no %s there, and it is not a git checkout",
			shortPath(dir), groups.ConfigName),
		"`oberth init` writes one; run status inside a directory that contains it")
}

// refreshConfigIndex asks the daemon to read dir's oberth.yaml into its index —
// the read-once that brings a manifest into the list (UPSTREAM). `status` does
// this when the daemon has lost a project's path, which is what a renamed or
// moved directory leaves behind. It reports whether a manifest existed to read;
// a registration that fails is not worth failing the whole read over.
func refreshConfigIndex(ctx context.Context, dir string) bool {
	c := daemonClient(ctx)
	if c == nil {
		return false // no daemon: the direct-scan path is its own authority
	}
	defer c.Close()
	path := manifestAt(dir)
	if path == "" {
		return false
	}
	var res rpc.GroupsConfigGetResult
	if err := c.Call(ctx, "groups.config.get", rpc.GroupsConfigGetParams{Path: &path}, &res); err != nil {
		return false
	}
	return true
}

// manifestAt is the path of a project's manifest in dir, if one exists. It
// prefers the current spelling and falls back to the legacy dotted one.
func manifestAt(dir string) string {
	for _, name := range []string{groups.ConfigName, groups.LegacyConfigName} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// buildStatusDocument is the read behind `status`, without the printing.
//
// mark is what `--no-mark` turns off: looking at the change without moving the
// anchor the next run compares against.
func buildStatusDocument(ctx context.Context, dir string, mark bool, only []string) (statusDocument, error) {
	name, root, err := statusProject(dir)
	if err != nil {
		return statusDocument{}, err
	}

	// One snapshot for everything the daemon knows: the group row already
	// carries the join between the manifest and reality (running, port_actual,
	// last_exit), and its ports are the "actually listening" half.
	pp, gg, obs, err := groupRowsObserved(ctx)
	if err != nil {
		return statusDocument{}, err
	}
	var group *state.Group
	for i := range gg {
		if gg[i].Name == name {
			group = &gg[i]
			break
		}
	}
	if group == nil {
		// The daemon may have lost this project: it only knows roots it has read
		// once (UPSTREAM), so a directory that was renamed or moved keeps its
		// manifest here while the daemon's index still points at the gone path.
		// Reading the manifest once brings it into the index; then re-read.
		if refreshConfigIndex(ctx, root) {
			pp, gg, obs, err = groupRowsObserved(ctx)
			if err != nil {
				return statusDocument{}, err
			}
			for i := range gg {
				if gg[i].Name == name {
					group = &gg[i]
					break
				}
			}
		}
		if group == nil {
			// A stopped project with no listener may not have reached the daemon's
			// long-lived index yet. Read the manifest directly so the primary
			// command works on a fresh worktree without a separate discovery step.
			if local, ok := localStatusGroup(root, name, pp); ok {
				group = local
			}
		}
		if group == nil {
			return statusDocument{}, failHint("not_found",
				fmt.Sprintf("no project named %q", name),
				"a project needs an oberth.yaml; `oberth init` writes one")
		}
	}
	if group.RootDir != nil && *group.RootDir != "" {
		root = *group.RootDir
	}
	// An older daemon does not publish the checkout identity yet; the value is
	// a pure function of the root, so status can still answer where to read it.
	if group.Identity == "" {
		group.Identity = groups.WorktreeTokenFor(root)
	}
	// This is the one expensive read status performs, and it is bounded and
	// limited to running services. The daemon's scan path never does it. A
	// scoped read resolves its selection first so services outside `--only`
	// are never hashed.
	var codeSelection map[string]bool
	if len(only) > 0 {
		// An invalid selection is reported later, after the mark, exactly as
		// `--only` did before code facts existed. Here it only means there is
		// no selection to narrow the expensive read.
		if selected, selErr := statusOnlySelection(group.Services, only); selErr == nil {
			codeSelection = selected
		}
	}
	annotateCodeFacts(ctx, group, codeSelection)

	ports := make([]state.Port, 0, len(pp))
	for _, p := range pp {
		if p.Group == name {
			ports = append(ports, state.FromListening(p))
		}
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })

	// Git shells out and is the only part that can be slow; a project outside a
	// repository simply has none of it. The mark keeps the full file list, while
	// the printed document gets a bounded prefix: truncating before the ledger
	// would turn later files into "gone" on the next comparison.
	var tree *git.Tree
	var fullTree git.Tree
	if t, err := git.ReadTree(ctx, root); err == nil {
		fullTree = t
		capped := t
		capped.CapFiles(statusFilesShown)
		tree = &capped
	}

	doc := statusDocument{
		Scope:       display.Scope{Kind: "worktree", Name: name, Root: root},
		Worktree:    *group,
		Ports:       ports,
		Git:         tree,
		Drafts:      []draft.Pending{},
		Exits:       statusExits(ctx, name),
		Observation: publicObservation(obs),
	}
	// The direct read (no daemon) has no snapshot of its own, and the daemon's
	// runs.list may be older than the service row. Fill only what is still
	// missing, from the full scan rather than doc.Ports: the occupier may
	// belong to another worktree.
	applyExitPortHolders(doc.Exits, pp)
	if pending, ok := draft.PendingFor(name); ok {
		doc.Drafts = append(doc.Drafts, pending)
	}
	publishAttention(&doc)

	// The mark and the comparison. Looking without marking still compares — it
	// just does not move the anchor, which is what an agent wants when it is
	// looking at the shape rather than at the project.
	seen := loadStatusSeen()
	entry := statusSeenFrom(root, *group, ports, fullTree, time.Now())
	if mark {
		before, had := seen.mark(root, entry)
		if had {
			change := compareStatusSeen(before, entry)
			doc.Changed = &change
		}
	} else if before, had := seen.Roots[root]; had {
		change := compareStatusSeen(before, entry)
		doc.Changed = &change
	}

	return doc, nil
}

// publishAttention makes the deterministic anomaly visible to an optional
// agent adapter without putting a model or network call in status' read path.
// Failure to publish the optional artifact never makes the facts unavailable.
func publishAttention(doc *statusDocument) string {
	if doc == nil {
		return ""
	}
	doc.AttentionPath = ""
	doc.AttentionRevision = ""
	facts := attention.Facts{
		WorktreeRoot: doc.Scope.Root,
		Branch:       doc.Worktree.Branch,
		Services:     make([]attention.Service, 0, len(doc.Worktree.Services)),
		Machine:      make([]attention.Machine, 0, len(doc.Worktree.Machine)),
	}
	for _, service := range doc.Worktree.Services {
		item := attention.Service{
			Name:    service.Name,
			Running: service.Running,
		}
		if service.HealthStatus != nil {
			item.HealthStatus = service.HealthStatus.Status
			item.HealthCode = service.HealthStatus.Code
			item.HealthReason = service.HealthStatus.Reason
		}
		if service.Port != nil {
			port := *service.Port
			item.DeclaredPort = &port
		}
		if service.PortActual != nil {
			port := *service.PortActual
			item.ActualPort = &port
		}
		if service.LogPath != nil {
			item.LogPath = *service.LogPath
		}
		if service.RunID != nil {
			item.RunID = *service.RunID
		}
		if service.StartedAt != nil {
			item.StartedAt = *service.StartedAt
		}
		if service.LastExit != nil {
			item.LastExit = &attention.Exit{
				Code:   service.LastExit.Code,
				Reason: service.LastExit.Reason,
				RunID:  service.LastExit.RunID,
			}
			if at, err := time.Parse(time.RFC3339Nano, service.LastExit.At); err == nil {
				item.LastExit.At = at
			}
		}
		facts.Services = append(facts.Services, item)
	}
	for _, machine := range doc.Worktree.Machine {
		facts.Machine = append(facts.Machine, attention.Machine{
			Name: machine.Name, Port: machine.Port, Listening: machine.Listening,
		})
	}
	for _, port := range doc.Ports {
		item := attention.Listener{Port: port.Port, Cwd: port.Cwd}
		if port.ProjectRoot != nil {
			item.ProjectRoot = *port.ProjectRoot
		}
		facts.Listeners = append(facts.Listeners, item)
	}
	for _, run := range doc.Exits {
		at, err := time.Parse(time.RFC3339Nano, run.ExitedAt)
		if err != nil {
			continue
		}
		facts.History = append(facts.History, attention.ExitHistory{
			Service: run.Name, Reason: run.Reason, RunID: run.ID, At: at,
		})
	}
	facts.StateRevision = attention.Revision(facts)
	facts.ObservedAt = time.Now().UTC()
	artifact, ok := attention.Derive(facts, facts.ObservedAt, attention.DefaultTTL)
	if !ok {
		_ = attention.Clear(paths.Dir(), doc.Scope.Root)
		return ""
	}
	path, err := attention.Write(paths.Dir(), artifact)
	if err != nil {
		return ""
	}
	doc.AttentionPath = path
	doc.AttentionRevision = artifact.StateRevision
	return path
}

// localStatusGroup is the cold-start fallback for a manifest the daemon has
// not indexed yet. It uses the same resolver and state conversion as the
// normal scan path, but does not pretend that a local read knows daemon run
// history; that history is added as soon as the daemon sees the project.
func localStatusGroup(root, name string, pp []ports.ListeningPort) (*state.Group, bool) {
	index := groups.NewIndex()
	index.Observe(root)
	rows := make([]state.Port, 0, len(pp))
	for _, p := range pp {
		rows = append(rows, state.FromListening(p))
	}
	// Include local run history: when the listener table is unavailable this
	// is the only evidence that a declared service was started and is alive.
	for _, candidate := range groups.GroupsWith(rows, index, directRunHistory()) {
		if candidate.Name == name {
			copy := candidate
			return &copy, true
		}
	}
	return nil, false
}

// publicObservation keeps the healthy wire shape unchanged: an observable
// process has no observation object. Any other verdict is a fact the reader
// needs before treating an empty port list as idle.
func publicObservation(obs *rpc.ObservationStatus) *rpc.ObservationStatus {
	if obs == nil || obs.State == string(ports.ObservationObservable) {
		return nil
	}
	return obs
}

// statusExits is the finished runs of one group, newest first. A registry that
// is gone (no daemon) has nothing to say, and that is not an error: the rest of
// the answer stands on its own.
func statusExits(ctx context.Context, group string) []rpc.RunRecord {
	records := []rpc.RunRecord{}
	c := daemonClient(ctx)
	if c == nil {
		return statusExitsDirect(group)
	}
	defer c.Close()
	var out rpc.RunsListResult
	if err := c.Call(ctx, "runs.list", rpc.Empty{}, &out); err != nil {
		return records
	}
	for _, run := range out.Exited {
		if run.Group == group {
			records = append(records, run)
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		return exitTime(records[i]).After(exitTime(records[j]))
	})
	if len(records) > statusExitsShown {
		records = records[:statusExitsShown]
	}
	applyExitHints(records)
	return records
}

// statusExitsDirect reads the daemon's durable exit history without opening it
// for writes. A stopped daemon still leaves the local status command able to
// answer why a declared service is down.
func statusExitsDirect(group string) []rpc.RunRecord {
	history, err := store.OpenReadOnly("")
	if err != nil {
		return []rpc.RunRecord{}
	}
	defer history.Close()
	rows, err := history.RunExits(0)
	if err != nil {
		return []rpc.RunRecord{}
	}
	records := make([]rpc.RunRecord, 0, len(rows))
	for _, row := range rows {
		if row.Group != group {
			continue
		}
		code := row.Code
		record := rpc.RunRecord{
			ID: row.ID, PID: row.PID, Group: row.Group, Name: row.Name,
			Cmd: row.Cmd, Cwd: row.Cwd, StartedAt: row.StartedAt.UTC().Format(time.RFC3339),
			Ports: []int{}, Status: "exited", ConfigPath: row.ConfigPath,
			StartID: row.StartID, Origin: row.Origin, LogPath: row.LogPath,
			LogOffset: &row.LogOffset,
			ExitCode:  &code, Reason: row.Reason,
			ExitedAt:  row.ExitedAt.UTC().Format(time.RFC3339),
			LastLines: append([]string{}, row.LastLines...),
		}
		if row.PortHint > 0 {
			hint := row.PortHint
			record.PortHint = &hint
			record.URL = "http://localhost:" + strconv.Itoa(hint)
		}
		records = append(records, record)
	}
	if len(records) > statusExitsShown {
		records = records[:statusExitsShown]
	}
	applyExitHints(records)
	return records
}

// exitHint is the next command for one finished run, or empty when the run
// left no log to read. It is a file-existence check, not a claim that the
// process ever started: a spawn failure after the log was opened has a file,
// and a deleted or rotated-away log has none.
func exitHint(run rpc.RunRecord) string {
	if run.Name == "" || !isFailureReason(run.Reason) || run.LogPath == "" {
		return ""
	}
	info, err := os.Stat(run.LogPath)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return logsHint(run.Name)
}

// applyExitHints fills a next step the daemon did not supply. Only empty fields
// are written, so a newer daemon's richer hint wins.
func applyExitHints(records []rpc.RunRecord) {
	for i := range records {
		if records[i].Hint == "" {
			records[i].Hint = exitHint(records[i])
		}
	}
}

// applyExitPortHolders fills port_holder evidence that the read path could not
// attach. records is modified in place; a holder a daemon already joined from
// its own snapshot is never overwritten. listening is the full scan, not the
// worktree's own ports: the occupier may belong to another worktree.
func applyExitPortHolders(records []rpc.RunRecord, listening []ports.ListeningPort) {
	var rows []state.Port
	for _, p := range listening {
		rows = append(rows, state.FromListening(p))
	}
	for i := range records {
		run := &records[i]
		if run.PortHolder != nil || run.Reason != "port_occupied" || run.PortHint == nil {
			continue
		}
		run.PortHolder = servicefacts.FindPortHolder(rows, servicefacts.PortHolderQuery{
			Port: *run.PortHint, Group: run.Group, Service: run.Name, RunID: run.ID,
		})
	}
}

// portHolderNote renders who holds the failed port in the fewest words that
// still answer "may I stop it?".
func portHolderNote(holder *state.PortHolder, group string) string {
	if holder == nil {
		return ""
	}
	who := holder.Command
	if who == "" {
		who = "a process"
	}
	if holder.PID > 0 {
		who += fmt.Sprintf(" (pid %d)", holder.PID)
	}
	switch holder.Kind {
	case state.PortHolderRun:
		where := "another worktree's option-berth service"
		if holder.Group == group {
			where = "this worktree's option-berth service"
		}
		who += " · " + where
		if holder.Name != "" {
			who += " " + holder.Name
		}
	case state.PortHolderPrevious:
		who += " · previous run of this service"
	case state.PortHolderListener:
		where := "a listener attributed to another worktree"
		if holder.Group == group {
			where = "a listener in this worktree"
		}
		who += " · " + where
		if holder.Group != "" {
			who += " (" + holder.Group + ")"
		}
	default:
		who += " · ownership unknown"
	}
	return who
}
func exitTime(run rpc.RunRecord) time.Time {
	if at, err := time.Parse(time.RFC3339, run.ExitedAt); err == nil {
		return at
	}
	return time.Time{}
}

// printStatus renders the aggregate for a person.
//
// The labels are a fixed-width column, one line each — the same reading posture
// as a reading table, so a glance down the left edge answers "what kinds of
// things are in here" before any of it is read. Nothing is phrased as a
// verdict: the change line lists what moved, not whether it should have.
func printStatus(doc statusDocument) {
	printStatusHead(doc)
	printStatusServices(doc)
	printStatusMachine(doc)
	printStatusResources(doc)
	printStatusUnclaimedPorts(doc)
	printStatusCode(doc)
	printStatusFailure(doc)
	printStatusDraft(doc)
	printStatusChange(doc)
}

// printStatusBrief is the human half of --brief: the same service, dependency
// and failure rows, without the repository summary, drafts or change line. It
// excludes no fact that explains whether a service is running.
func printStatusBrief(doc statusDocument) {
	printStatusHead(doc)
	printStatusServices(doc)
	printStatusMachine(doc)
	printStatusResources(doc)
	printStatusUnclaimedPorts(doc)
	printStatusFailure(doc)
}

func printStatusHead(doc statusDocument) {
	head := display.Bold(doc.Worktree.Name)
	if doc.Scope.Root != "" {
		head += "  " + display.Dim(shortPath(doc.Scope.Root))
	}
	if doc.Worktree.Branch != "" {
		head += "  " + display.Dim(doc.Worktree.Branch)
	}
	fmt.Println(head)

	if doc.Observation != nil {
		statusLine("listeners", display.Yellow("unknown")+" · "+doc.Observation.Detail)
		if doc.Observation.Hint != "" {
			statusLine("next", doc.Observation.Hint)
		}
	}
}

// statusLabels are every label the reading table can carry. The column width
// is measured once from the widest, in terminal cells rather than runes, so
// every row's value starts at the same cell.
var statusLabels = []string{
	"services", "depends on", "listeners", "resources", "code", "failed", "next", "drafts", "last look",
}

func statusLabelWidth() int {
	w := 0
	for _, l := range statusLabels {
		if n := display.DisplayWidth(l); n > w {
			w = n
		}
	}
	return w
}

func statusLine(label, value string) {
	fmt.Printf("  %s  %s\n", display.PadDisplay(display.Dim(label), statusLabelWidth()), value)
}

// printStatusServices answers "what is supposed to run, and is it".
func printStatusServices(doc statusDocument) {
	if len(doc.Worktree.Services) == 0 {
		statusLine("services", display.Dim("none declared — `oberth init` writes the starter, `oberth init draft` asks an agent"))
		return
	}
	for _, s := range doc.Worktree.Services {
		parts := []string{s.Name, serviceStateText(s)}
		if s.PortActual != nil {
			parts = append(parts, "listening on "+fmt.Sprint(*s.PortActual))
		} else if s.Port != nil {
			parts = append(parts, "declared "+fmt.Sprint(*s.Port))
		} else if s.PortAuto {
			parts = append(parts, "auto port unassigned")
		} else if s.Running {
			parts = append(parts, "no port")
		}
		if s.PID != nil {
			parts = append(parts, "pid "+fmt.Sprint(*s.PID))
		}
		if s.Stats != nil {
			parts = append(parts,
				fmt.Sprintf("cpu %.1f%%", s.Stats.CPUPercent),
				"mem "+ports.FormatBytes(s.Stats.MemoryRSS))
		}
		if s.LastExit != nil && s.LastExit.PortHolder != nil && s.LastExit.PortHolder.PID > 0 {
			parts = append(parts, "port held by pid "+fmt.Sprint(s.LastExit.PortHolder.PID))
		}
		codeChanged := s.CodeChangedSinceStart != nil && *s.CodeChangedSinceStart
		if codeChanged {
			detail := "code changed since start"
			if s.CodeChangedFiles != nil {
				detail += fmt.Sprintf(" (%d %s)", *s.CodeChangedFiles, pluralWord(*s.CodeChangedFiles, "file"))
			}
			parts = append(parts, display.Yellow(detail))
		}
		statusLine("services", strings.Join(parts, " · "))
		if codeChanged && s.CodeHint != nil {
			statusLine("next", display.Dim(*s.CodeHint))
		}
	}
}

// serviceStateText is the state word for one service, colored so a column of
// them can be swept: running is green, failure red, routine stops dim. The
// word carries the meaning on its own — color is the second channel, not the
// only one, so NO_COLOR loses nothing.
func serviceStateText(s state.Service) string {
	if s.Running {
		return display.Green("running")
	}
	if s.Starting != nil {
		// A start is in flight. This is a transient fact from the start call,
		// not a run: name the phase instead of claiming the service is idle.
		return display.Dim(startingStateText(*s.Starting))
	}
	if s.LastExit == nil {
		return display.Dim("not running")
	}
	switch s.LastExit.Reason {
	case "stopped":
		return display.Dim("stopped")
	case "exited":
		return display.Dim("exited")
	case "port_occupied":
		return display.Red(fmt.Sprintf("failed · port occupied · exit %d", s.LastExit.Code))
	case "start_failed":
		return display.Red(fmt.Sprintf("failed · start failed · exit %d", s.LastExit.Code))
	case "crashed":
		return display.Red(fmt.Sprintf("failed · crashed · exit %d", s.LastExit.Code))
	default:
		return display.Red(fmt.Sprintf("failed · %s · exit %d", s.LastExit.Reason, s.LastExit.Code))
	}
}

// startingStateText is the words for an in-flight start attempt:
// `starting · prepare · for 2m10s · budget 3m0s · waiting on db, cache`.
//
// The extra parts answer the question a reader actually has — "is this stuck?"
// — which needs how long the attempt has been going and the budget it is being
// measured against. elapsed_ms is read-time arithmetic, so it is rounded to
// whole seconds here rather than printed to the millisecond; ready_timeout is
// the daemon's published value and is not reformatted.
func startingStateText(starting state.ServiceStarting) string {
	parts := []string{"starting"}
	if starting.Phase != "" {
		parts = append(parts, starting.Phase)
	}
	if starting.ElapsedMS > 0 {
		elapsed := (time.Duration(starting.ElapsedMS) * time.Millisecond).Round(time.Second)
		if elapsed < time.Second {
			// Rounding must not turn a fresh attempt into "for 0s".
			parts = append(parts, "for <1s")
		} else {
			parts = append(parts, "for "+elapsed.String())
		}
	}
	if starting.ReadyTimeout != "" {
		parts = append(parts, "budget "+starting.ReadyTimeout)
	}
	if len(starting.WaitingOn) > 0 {
		parts = append(parts, "waiting on "+strings.Join(starting.WaitingOn, ", "))
	}
	return strings.Join(parts, " · ")
}

// printStatusMachine is the same declaration-versus-reality pair as the ports
// line, for the services the project needs but does not own (decision 0010):
// the manifest says it depends on them, and this says whether they are there.
func printStatusMachine(doc statusDocument) {
	if len(doc.Worktree.Machine) == 0 {
		return
	}
	parts := make([]string, 0, len(doc.Worktree.Machine))
	for _, m := range doc.Worktree.Machine {
		who := fmt.Sprintf("%s %d", m.Name, m.Port)
		if m.Listening {
			parts = append(parts, who+" listening")
		} else {
			parts = append(parts, who+" not listening")
		}
	}
	statusLine("depends on", strings.Join(parts, " · "))
}

// printStatusResources is the worktree-level sum. "Not measured" stays a
// different word from a measured zero: a running service with no sample is
// counted, but it is not silently added as 0%.
func printStatusResources(doc statusDocument) {
	running := doc.Worktree.RunningServices
	if running == 0 && doc.Worktree.Resources == nil {
		return
	}
	parts := []string{fmt.Sprintf("%d running", running)}
	if res := doc.Worktree.Resources; res != nil {
		parts = append(parts,
			fmt.Sprintf("cpu %.1f%%", res.CPUPercent),
			"mem "+ports.FormatBytes(res.MemoryRSS))
		if res.Services != running {
			parts = append(parts, fmt.Sprintf("%d sampled", res.Services))
		}
	}
	statusLine("resources", strings.Join(parts, " · "))
}

// printStatusUnclaimedPorts keeps the normal status path service-centered. A
// listener with no matching service is the exceptional fact that cannot fit on
// any service row, so it gets its own line only when one exists.
func printStatusUnclaimedPorts(doc statusDocument) {
	declared := []int{}
	for _, s := range doc.Worktree.Services {
		for _, port := range []*int{s.Port, s.PortActual} {
			if port != nil && !slices.Contains(declared, *port) {
				declared = append(declared, *port)
			}
		}
	}
	slices.Sort(declared)

	unclaimed := []int{}
	for _, p := range doc.Ports {
		if !slices.Contains(declared, p.Port) {
			unclaimed = append(unclaimed, p.Port)
		}
	}
	if len(unclaimed) == 0 {
		return
	}
	names := make([]string, 0, len(unclaimed))
	for _, p := range unclaimed {
		names = append(names, strconv.Itoa(p))
	}
	statusLine("listeners", strings.Join(names, ", ")+" — no service declares "+pluralWord(len(unclaimed), "it"))
}

// printStatusCode is the repository half: how much moved, not which lines.
func printStatusCode(doc statusDocument) {
	if doc.Git == nil {
		statusLine("code", display.Dim("not a git checkout"))
		return
	}
	snap := doc.Git.Snapshot
	parts := make([]string, 0, 6)
	for _, c := range []struct {
		count int
		label string
	}{
		{snap.Staged, "staged"}, {snap.Unstaged, "unstaged"},
		{snap.Untracked, "untracked"}, {snap.Conflicts, "conflicted"},
	} {
		if c.count > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", c.label, c.count))
		}
	}
	if snap.Ahead > 0 {
		parts = append(parts, fmt.Sprintf("ahead %d", snap.Ahead))
	}
	if snap.Behind > 0 {
		parts = append(parts, fmt.Sprintf("behind %d", snap.Behind))
	}
	if snap.Detached {
		parts = append(parts, "detached HEAD")
	}
	if len(parts) == 0 {
		parts = append(parts, "clean")
	}
	if snap.Last != nil {
		parts = append(parts, display.Dim(snap.Last.Hash+" "+snap.Last.Subject))
	}
	statusLine("code", strings.Join(parts, " · "))
}

// printStatusFailure is C3's "上次为何失败". It shows one row per service: the
// newest exit, and only while that exit is still the service's published
// last_exit. A service that is running again, or whose newest exit is a
// routine stop, is not currently failed; its retained history stays in exits[].
func printStatusFailure(doc statusDocument) {
	for _, run := range currentFailures(doc) {
		code := "—"
		if run.ExitCode != nil {
			code = fmt.Sprint(*run.ExitCode)
		}
		line := lastNonEmpty(run.LastLines)
		if run.Reason == "port_occupied" && run.PortHolder != nil {
			// The holder answers the question the last log line only raises.
			line = fmt.Sprintf("port %d held by %s", run.PortHolder.Port,
				portHolderNote(run.PortHolder, doc.Worktree.Name))
		}
		if line == "" {
			statusLine("failed", fmt.Sprintf("%s exit %s · no log lines kept", run.Name, code))
		} else {
			statusLine("failed", fmt.Sprintf("%s exit %s · %s", run.Name, code, display.Dim(line)))
		}
		if run.Hint != "" {
			statusLine("next", display.Dim(run.Hint))
		}
	}
}

// currentFailures is the failed section's set: the newest exit of each
// service, kept only while that exit is still the service's published
// last_exit. Deduplicating before the failure test also drops a service whose
// newest exit is a routine stop, even if an older crash is still retained.
// A service the manifest does not declare has no last_exit to compare; its
// newest retained exit is the best fact available.
func currentFailures(doc statusDocument) []rpc.RunRecord {
	services := make(map[string]*state.Service, len(doc.Worktree.Services))
	for i := range doc.Worktree.Services {
		services[doc.Worktree.Services[i].Name] = &doc.Worktree.Services[i]
	}
	exits := append([]rpc.RunRecord(nil), doc.Exits...)
	sort.SliceStable(exits, func(i, j int) bool {
		return exitTime(exits[i]).After(exitTime(exits[j]))
	})
	seen := make(map[string]bool, len(exits))
	out := make([]rpc.RunRecord, 0, len(exits))
	for _, run := range exits {
		if seen[run.Name] {
			continue
		}
		seen[run.Name] = true
		if !isFailureReason(run.Reason) {
			continue
		}
		if svc, declared := services[run.Name]; declared {
			if svc.LastExit == nil || svc.LastExit.RunID != run.ID {
				continue
			}
		}
		out = append(out, run)
	}
	return out
}

// isFailureReason keeps routine exits out of the failure section. A stopped
// process can have a non-zero signal code (for example 137 after --force),
// but that code records how it stopped, not a service failure.
func isFailureReason(reason string) bool {
	switch reason {
	case "stopped", "exited":
		return false
	default:
		return true
	}
}

func lastNonEmpty(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func printStatusDraft(doc statusDocument) {
	if len(doc.Drafts) == 0 {
		return
	}
	for _, p := range doc.Drafts {
		who := p.Agent
		if who == "" {
			who = "an unknown agent"
		}
		when := p.At
		if when != "" && len(when) >= 16 {
			when = when[5:] // "09-23 16:58": the year is not what a glance needs
		}
		statusLine("drafts", fmt.Sprintf("%s %s · waiting — `oberth init adopt`", who, when))
	}
}

// printStatusChange is the second run's half. 「没有基线」和「一条都没动」不能
// 长得一样（GUI 那条教训），所以第一次跑也要说话。
// printStatusChange is the second run's half. "No baseline" and "nothing
// moved" must not look the same (the GUI lesson), so the first run speaks too.
func printStatusChange(doc statusDocument) {
	if doc.Changed == nil {
		statusLine("last look", display.Dim("first look recorded — the next run compares against it"))
		return
	}
	at, err := time.Parse(time.RFC3339, doc.Changed.At)
	if err != nil {
		at = time.Now()
	}
	statusLine("last look", seenSummary(*doc.Changed, at))
}
