package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/git"
	"github.com/spf13/cobra"
)

var (
	gitJSONFlag      bool
	gitFilesJSONFlag bool
	gitDiffJSONFlag  bool
	gitDiffFileFlag  string
)

var gitCmd = &cobra.Command{
	Use:     "git [path]",
	Short:   "Show a repository's current state (read-only)",
	GroupID: commandGroupSupport,
	Long: "Show the branch, the worktrees, what has changed and where the checkout\n" +
		"stands against its upstream. With no argument it reads the repository the\n" +
		"working directory is in.\n\n" +
		"Two subcommands go further: `files` lists which files changed and by how\n" +
		"many lines, and `diff` shows what they say line by line.\n\n" +
		"This command only ever reads: option-berth does not commit, stage, switch\n" +
		"branches or push. Writing code has better authors — see the write boundary\n" +
		"in docs/product.md.\n\n" +
		"Exit codes:\n" +
		"  0  the directory is in a repository\n" +
		"  1  it is not, or git could not be asked",
	Args: cobra.MaximumNArgs(1),
	RunE: gitRun,
}

var gitFilesCmd = &cobra.Command{
	Use:   "files [path]",
	Short: "List the files that differ from HEAD, with line counts (read-only)",
	Long: "One row per changed path: git's own two-letter status, then how many lines\n" +
		"the file gained and lost against HEAD (staged and unstaged together).\n\n" +
		"The whole overview comes with it — branch, worktrees, counts — because a\n" +
		"caller that wants the list wants to know what it is a list of.\n\n" +
		"Counts absent mean there is no number to give: a binary file, a conflict,\n" +
		"an untracked file too large to count. Absent is not zero.\n\n" +
		"Exit codes:\n" +
		"  0  the directory is in a repository\n" +
		"  1  it is not, or git could not be asked",
	Args: cobra.MaximumNArgs(1),
	RunE: gitFilesRun,
}

var gitDiffCmd = &cobra.Command{
	Use:   "diff [path]",
	Short: "Show what changed, line by line (read-only)",
	Long: "Every line git prints, each marked as added, removed, context, a hunk header\n" +
		"or file metadata — with the marker left in the text, so nothing is lost\n" +
		"between git and whoever reads this.\n\n" +
		"--file names one path **relative to the repository root** (the shape\n" +
		"`oberth git files` prints), or an absolute path. Without it, every\n" +
		"changed file is read — one git call each, so on a large change set naming\n" +
		"the file you want is much cheaper.\n\n" +
		"Exit codes:\n" +
		"  0  there is a patch, or nothing changed at all\n" +
		"  1  not a repository, git could not be asked, no such path, or that path\n" +
		"     has no changes to show",
	Args: cobra.MaximumNArgs(1),
	RunE: gitDiffRun,
}

func init() {
	gitCmd.Flags().BoolVar(&gitJSONFlag, "json", false, "Output as JSON")
	gitFilesCmd.Flags().BoolVar(&gitFilesJSONFlag, "json", false, "Output as JSON")
	gitDiffCmd.Flags().BoolVar(&gitDiffJSONFlag, "json", false, "Output as JSON")
	gitDiffCmd.Flags().StringVar(&gitDiffFileFlag, "file", "",
		"Only this path, relative to the repository root")
	gitCmd.AddCommand(gitFilesCmd, gitDiffCmd)
	rootCmd.AddCommand(gitCmd)
}

func gitRun(cmd *cobra.Command, args []string) error {
	dir, err := gitDir(args)
	if err != nil {
		return err
	}

	snap, err := git.Read(cmd.Context(), dir)
	if err != nil {
		return gitFail(err, dir)
	}

	if gitJSONFlag {
		return printJSON(snap)
	}
	renderGit(os.Stdout, snap)
	return nil
}

func gitFilesRun(cmd *cobra.Command, args []string) error {
	dir, err := gitDir(args)
	if err != nil {
		return err
	}

	tree, err := git.ReadTree(cmd.Context(), dir)
	if err != nil {
		return gitFail(err, dir)
	}

	if gitFilesJSONFlag {
		return printJSON(tree)
	}
	renderGitFiles(os.Stdout, tree)
	return nil
}

func gitDiffRun(cmd *cobra.Command, args []string) error {
	dir, err := gitDir(args)
	if err != nil {
		return err
	}

	patch, err := readGitDiff(cmd.Context(), dir, gitDiffFileFlag)
	if err != nil {
		return err
	}

	if gitDiffJSONFlag {
		return printJSON(patch)
	}
	renderGitDiff(os.Stdout, patch)
	return nil
}

// readGitDiff is the read behind `git diff --file`, with the package's two
// sentinels turned into the codes a caller branches on.
func readGitDiff(ctx context.Context, dir, file string) (git.Patch, error) {
	patch, err := git.Diff(ctx, dir, file)
	switch {
	case errors.Is(err, git.ErrNoChanges):
		return git.Patch{}, failHint("not_found",
			fmt.Sprintf("%s has no changes to show", file),
			"the file is there, but it differs from HEAD in no way")
	case errors.Is(err, git.ErrNoSuchFile):
		return git.Patch{}, failHint("not_found",
			fmt.Sprintf("%s is not in that repository", file),
			"paths are named relative to the repository root — the shape `oberth git files` prints")
	case err != nil:
		return git.Patch{}, gitFail(err, dir)
	}
	return patch, nil
}

// gitDir is the directory a git command was pointed at: the argument, or the
// working directory when there is none.
func gitDir(args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	return os.Getwd()
}

// gitFail maps the package's sentinels onto the CLI's error codes. Both readers
// get the same wording, whoever asked.
func gitFail(err error, dir string) error {
	switch {
	case errors.Is(err, git.ErrNotARepository):
		return failHint("not_found",
			fmt.Sprintf("%s is not in a git repository", shortPath(dir)),
			"run it inside a checkout, or name one: `oberth git <path>`")
	case errors.Is(err, git.ErrGitUnavailable):
		return failHint("unsupported_platform",
			"git is not on PATH",
			"install git, or check that the PATH this shell sees has it")
	}
	return cliError(err)
}

// renderGit prints a snapshot the way a person reads it: where, what branch,
// what moved, and — when there is more than one — where the other berths are.
func renderGit(w io.Writer, s git.Snapshot) {
	fmt.Fprintln(w, display.Dim(shortPath(s.Root)))
	fmt.Fprintln(w, gitBranchLine(s))
	fmt.Fprintln(w, gitChangesLine(s))
	if s.Last != nil {
		fmt.Fprintln(w, gitLastLine(*s.Last))
	}

	// One worktree is the normal case and naming it adds nothing; several is
	// exactly when a person needs to know which one they are standing in
	// (docs/product.md: parallel berths).
	if len(s.Worktrees) > 1 {
		fmt.Fprintf(w, "\n%s\n", display.Dim("berths"))
		for _, wt := range s.Worktrees {
			mark := " "
			if wt.Current {
				mark = "→"
			}
			branch := wt.Branch
			if wt.Detached || branch == "" {
				branch = "detached"
			}
			fmt.Fprintf(w, "  %s %s  %s\n", mark, shortPath(wt.Path), display.Dim(branch))
		}
	}
}

// renderGitFiles prints the tree the way a person scans it: the same groups
// `git status` uses, one row per file, the count out where two rows can be
// compared without reading either path.
func renderGitFiles(w io.Writer, t git.Tree) {
	fmt.Fprintln(w, display.Dim(shortPath(t.Root)))
	fmt.Fprintln(w, gitBranchLine(t.Snapshot))
	if len(t.Files) == 0 {
		fmt.Fprintln(w, display.Dim("clean"))
		return
	}

	groups := groupFiles(t.Files)
	names, adds, dels := groupWidths(groups)
	for _, g := range groups {
		if len(g.files) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s\n", display.Dim(g.label))
		for _, f := range g.files {
			renderFileRow(w, f, names, adds, dels)
		}
	}
}

// renderGitDiff prints a patch the way a person reads one: git's own text, with
// the markers colored and the file headers dimmed.
func renderGitDiff(w io.Writer, p git.Patch) {
	if len(p.Files) == 0 {
		fmt.Fprintln(w, display.Dim("clean"))
		return
	}
	for i, f := range p.Files {
		if i > 0 {
			fmt.Fprintln(w)
		}
		for _, l := range f.Lines {
			fmt.Fprintln(w, colorDiffLine(l))
		}
	}
}

func colorDiffLine(l git.PatchLine) string {
	switch l.Kind {
	case git.KindAdd:
		return display.Green(l.Text)
	case git.KindDel:
		return display.Red(l.Text)
	case git.KindHunk:
		return display.Cyan(l.Text)
	case git.KindMeta:
		return display.Dim(l.Text)
	}
	return l.Text
}

// fileGroup is one heading of the list.
type fileGroup struct {
	label string
	files []git.File
}

// groupFiles splits the rows the way `git status` does: what is staged, what
// only the working tree has, what is conflicted, what git has never seen.
//
// A file with work on both sides appears in both of the first two groups — it
// is two things that need doing, and `git status` lists it under both as well.
func groupFiles(files []git.File) []fileGroup {
	groups := []fileGroup{
		{label: "staged"},
		{label: "not staged"},
		{label: "conflicted"},
		{label: "untracked"},
	}
	for _, f := range files {
		switch {
		case f.Untracked():
			groups[3].files = append(groups[3].files, f)
		case conflicted(f.Status):
			groups[2].files = append(groups[2].files, f)
		default:
			if indexSide(f.Status) != ' ' {
				groups[0].files = append(groups[0].files, f)
			}
			if worktreeSide(f.Status) != ' ' {
				groups[1].files = append(groups[1].files, f)
			}
		}
	}
	return groups
}

// fileWidths measures the three columns once, so every row lines up. Two spaces
// of gap go after the widest path, or the longest row has its count welded to
// the file name.
func fileWidths(files []git.File) (names, adds, dels int) {
	const maxName = 64
	for _, f := range files {
		if n := len([]rune(fileName(f))); n > names {
			names = n
		}
		if f.Additions != nil && len(fmt.Sprintf("+%d", *f.Additions)) > adds {
			adds = len(fmt.Sprintf("+%d", *f.Additions))
		}
		if f.Deletions != nil && len(fmt.Sprintf("−%d", *f.Deletions)) > dels {
			dels = len(fmt.Sprintf("−%d", *f.Deletions))
		}
	}
	if names > maxName {
		names = maxName
	}
	return names, adds, dels
}

// groupWidths is fileWidths over the grouped tree.
func groupWidths(groups []fileGroup) (names, adds, dels int) {
	for _, g := range groups {
		n, a, d := fileWidths(g.files)
		if n > names {
			names = n
		}
		if a > adds {
			adds = a
		}
		if d > dels {
			dels = d
		}
	}
	return names, adds, dels
}

// renderFileRow is one row of any file list — the tree, a commit's files. One
// place decides how a status, a path and two counts line up.
func renderFileRow(w io.Writer, f git.File, names, adds, dels int) {
	fmt.Fprintf(w, "  %s  %s%s\n",
		statusCode(f.Status),
		padRight(truncateMiddle(fileName(f), names), names+2),
		countsCell(f, adds, dels))
}

// countsCell is what sits on the right of a row: the two numbers, or the reason
// there are none. Widths come from groupWidths, and the coloring happens after
// the padding so the columns stay even.
func countsCell(f git.File, adds, dels int) string {
	switch {
	case f.Binary:
		return display.Dim(padLeft("binary", adds+1+dels))
	case f.Additions == nil || f.Deletions == nil:
		return ""
	}
	add := padLeft(fmt.Sprintf("+%d", *f.Additions), adds)
	del := padLeft(fmt.Sprintf("−%d", *f.Deletions), dels)
	return display.Green(add) + " " + display.Red(del)
}

// fileName is the path a row shows. A rename says where it came from, because
// "which file is this" is exactly the question a rename makes hard.
func fileName(f git.File) string {
	if f.OldPath != "" {
		return f.OldPath + " → " + f.Path
	}
	return f.Path
}

// statusCode colors git's two letters by what they mean: new is green, gone is
// red, moved is cyan, changed is yellow. It is the one place in this output
// where color carries information rather than polish.
func statusCode(status string) string {
	switch {
	case status == "??":
		return display.Green(status)
	case conflicted(status) || strings.Contains(status, "D"):
		return display.Red(status)
	case strings.Contains(status, "A"):
		return display.Green(status)
	case strings.Contains(status, "R"), strings.Contains(status, "C"):
		return display.Cyan(status)
	}
	return display.Yellow(status)
}

// conflicted reports whether a path is unmerged: git writes those pairs as UU,
// AU, UA, DU, UD — and AA or DD for both sides added or both deleted, which
// have no U in them.
func conflicted(status string) bool {
	return strings.Contains(status, "U") || status == "AA" || status == "DD"
}

func indexSide(status string) byte {
	if status == "" {
		return ' '
	}
	return status[0]
}

func worktreeSide(status string) byte {
	if len(status) < 2 {
		return ' '
	}
	return status[1]
}

// truncateMiddle shortens a path from the middle: the head says which tree it
// is in and the tail says which file, and those are the two ends a person reads.
func truncateMiddle(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width < 2 {
		return string(runes[:width])
	}
	keep := width - 1
	head := keep/2 + keep%2
	tail := keep - head
	return string(runes[:head]) + "…" + string(runes[len(runes)-tail:])
}

// padLeft is padRight's mirror for the numbers column, which is read
// right-aligned: "+108 −0" over "+4 −1" only lines up if the digits do.
func padLeft(s string, width int) string {
	if n := len([]rune(s)); n < width {
		return strings.Repeat(" ", width-n) + s
	}
	return s
}

// gitBranchLine is "main → origin/main +2 −1", or the commit when HEAD is
// detached. Only the non-zero side of ahead/behind is shown: "+2 −0" makes a
// person read two numbers to learn one thing.
func gitBranchLine(s git.Snapshot) string {
	branch := s.Branch
	if s.Detached || branch == "" {
		branch = "detached at " + shortSHA(s.Head)
	}
	if s.Upstream == "" {
		return branch
	}
	line := branch + " → " + s.Upstream
	var ab []string
	if s.Ahead > 0 {
		ab = append(ab, fmt.Sprintf("+%d", s.Ahead))
	}
	if s.Behind > 0 {
		ab = append(ab, fmt.Sprintf("−%d", s.Behind))
	}
	if len(ab) > 0 {
		line += " " + display.Dim(strings.Join(ab, " "))
	}
	return line
}

func gitChangesLine(s git.Snapshot) string {
	if s.Clean() {
		return display.Dim("clean")
	}
	var parts []string
	if s.Staged > 0 {
		parts = append(parts, fmt.Sprintf("%d staged", s.Staged))
	}
	if s.Unstaged > 0 {
		parts = append(parts, fmt.Sprintf("%d not staged", s.Unstaged))
	}
	if s.Untracked > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", s.Untracked))
	}
	if s.Conflicts > 0 {
		parts = append(parts, display.Red(fmt.Sprintf("%d conflicted", s.Conflicts)))
	}
	return strings.Join(parts, " · ")
}

func gitLastLine(c git.Commit) string {
	line := display.Dim(c.Hash) + "  " + c.Subject
	if when := shortWhen(c.When); when != "" {
		line += display.Dim("  " + when)
	}
	return line
}

// shortWhen renders a commit time as how long ago it was: the question a
// person asks about a checkout is "is this from just now or from last week".
func shortWhen(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
