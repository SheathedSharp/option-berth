package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/git"
	"github.com/spf13/cobra"
)

// runGit drives one of the git subcommands the way the CLI does — the same run
// function, a context and flags installed, stdout collected. Calling the run
// function directly is what keeps the test off the printer: what is being
// checked is the document, not the color.
func runGit(t *testing.T, cmd *cobra.Command, run func(*cobra.Command, []string) error, args ...string) (string, error) {
	t.Helper()
	cmd.SetContext(context.Background())
	var err error
	out := captureStdout(t, func() { err = run(cmd, args) })
	return out, err
}

// The tree command's document is the contract: the overview plus one row per
// changed path. The rows are the part a caller reads, so they are what the test
// reads too.
func TestGitFilesJSONCarriesTheRows(t *testing.T) {
	dir := gitTestRepo(t)
	chdir(t, dir)

	prev := gitFilesJSONFlag
	gitFilesJSONFlag = true
	t.Cleanup(func() { gitFilesJSONFlag = prev })

	out, err := runGit(t, gitFilesCmd, gitFilesRun)
	if err != nil {
		t.Fatalf("git files: %v", err)
	}

	var doc struct {
		Root      string `json:"root"`
		Branch    string `json:"branch"`
		Unstaged  int    `json:"unstaged"`
		Untracked int    `json:"untracked"`
		Files     []struct {
			Path      string `json:"path"`
			Status    string `json:"status"`
			Additions *int   `json:"additions"`
			Deletions *int   `json:"deletions"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not the documented document: %v\n%s", err, out)
	}
	if doc.Root != dir {
		t.Errorf("root = %q, want %q", doc.Root, dir)
	}
	if doc.Branch == "" {
		t.Error("branch is empty: the overview has to come with the list")
	}
	if doc.Unstaged != 1 || doc.Untracked != 1 {
		t.Errorf("counts = %d unstaged / %d untracked, want 1/1", doc.Unstaged, doc.Untracked)
	}
	if len(doc.Files) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(doc.Files), doc.Files)
	}

	kept := doc.Files[0]
	if kept.Path != "kept.txt" || kept.Status != " M" {
		t.Errorf("row 0 = %+v, want kept.txt not staged", kept)
	}
	if kept.Additions == nil || kept.Deletions == nil || *kept.Additions != 2 || *kept.Deletions != 1 {
		t.Errorf("kept.txt counts = %v/%v, want 2/1", kept.Additions, kept.Deletions)
	}
	fresh := doc.Files[1]
	if fresh.Path != "fresh.txt" || fresh.Status != "??" {
		t.Errorf("row 1 = %+v, want fresh.txt untracked", fresh)
	}
	// An untracked file is counted by reading it, and it did get a count.
	if fresh.Additions == nil || *fresh.Additions != 2 {
		t.Errorf("fresh.txt additions = %v, want 2", fresh.Additions)
	}
}

// A line-by-line answer, in the shape docs/cli.md documents: the kind is
// derived, the text is git's own word for word.
func TestGitGraphJSONCarriesParentsRefsAndLimit(t *testing.T) {
	dir := gitTestRepo(t)
	gitIn(t, dir, "add", "kept.txt")
	gitIn(t, dir, "commit", "-qm", "second")
	chdir(t, dir)

	prevJSON, prevLimit := gitGraphJSONFlag, gitGraphLimitFlag
	gitGraphJSONFlag, gitGraphLimitFlag = true, 1
	t.Cleanup(func() { gitGraphJSONFlag, gitGraphLimitFlag = prevJSON, prevLimit })

	out, err := runGit(t, gitGraphCmd, gitGraphRun)
	if err != nil {
		t.Fatalf("git graph: %v", err)
	}
	var doc struct {
		Root         string `json:"root"`
		ObservedHead string `json:"observed_head"`
		Branch       string `json:"branch"`
		Upstream     string `json:"upstream"`
		Ahead        int `json:"ahead"`
		Behind       int `json:"behind"`
		Truncated    bool `json:"truncated"`
		Limit        int `json:"limit"`
		Commits      []struct {
			Hash    string   `json:"hash"`
			Parents []string `json:"parents"`
			Subject string   `json:"subject"`
			Refs    []string `json:"refs"`
		} `json:"commits"`
		Refs []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
			Current bool `json:"current"`
		} `json:"refs"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not the documented graph document: %v\n%s", err, out)
	}
	if doc.Root != dir || doc.ObservedHead == "" || doc.Branch == "" || doc.Limit != 1 {
		t.Fatalf("identity = %+v, want root/head/limit", doc)
	}
	if len(doc.Commits) != 1 || !doc.Truncated {
		t.Fatalf("commits = %+v, want one commit and truncated=true", doc.Commits)
	}
	if len(doc.Commits[0].Parents) != 1 || doc.Commits[0].Subject != "second" {
		t.Fatalf("head commit = %+v, want parent and subject", doc.Commits[0])
	}
	foundCurrent := false
	for _, ref := range doc.Refs {
		if ref.Kind == "branch" && ref.Current { foundCurrent = true }
	}
	if !foundCurrent {
		t.Fatalf("refs = %+v, no current branch", doc.Refs)
	}
}

func TestGitDiffJSONCarriesTheLines(t *testing.T) {
	dir := gitTestRepo(t)
	chdir(t, dir)

	prevJSON, prevFile := gitDiffJSONFlag, gitDiffFileFlag
	gitDiffJSONFlag, gitDiffFileFlag = true, "kept.txt"
	t.Cleanup(func() { gitDiffJSONFlag, gitDiffFileFlag = prevJSON, prevFile })

	out, err := runGit(t, gitDiffCmd, gitDiffRun)
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}

	var doc struct {
		Root  string `json:"root"`
		Files []struct {
			Path  string `json:"path"`
			Lines []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"lines"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not the documented document: %v\n%s", err, out)
	}
	if len(doc.Files) != 1 || doc.Files[0].Path != "kept.txt" {
		t.Fatalf("files = %+v, want just kept.txt", doc.Files)
	}

	kinds := map[string]bool{}
	var added, removed string
	for _, l := range doc.Files[0].Lines {
		kinds[l.Kind] = true
		switch l.Kind {
		case git.KindAdd:
			if strings.Contains(l.Text, "+four") {
				added = l.Text
			}
		case git.KindDel:
			if strings.Contains(l.Text, "-two") {
				removed = l.Text
			}
		}
	}
	for _, want := range []string{git.KindMeta, git.KindHunk, git.KindContext, git.KindAdd, git.KindDel} {
		if !kinds[want] {
			t.Errorf("no %s line in the patch: %+v", want, kinds)
		}
	}
	if added != "+four" || removed != "-two" {
		t.Errorf("lines came back changed: added %q, removed %q", added, removed)
	}
}

func TestGitDiffCommitJSONCarriesHistoricalIdentity(t *testing.T) {
	dir := gitTestRepo(t)
	gitIn(t, dir, "add", "kept.txt")
	gitIn(t, dir, "commit", "-qm", "second")
	commitOut := gitInOutput(t, dir, "rev-parse", "HEAD")
	chdir(t, dir)

	prevJSON, prevFile, prevCommit := gitDiffJSONFlag, gitDiffFileFlag, gitDiffCommitFlag
	gitDiffJSONFlag, gitDiffFileFlag, gitDiffCommitFlag = true, "kept.txt", strings.TrimSpace(commitOut)
	t.Cleanup(func() { gitDiffJSONFlag, gitDiffFileFlag, gitDiffCommitFlag = prevJSON, prevFile, prevCommit })

	out, err := runGit(t, gitDiffCmd, gitDiffRun)
	if err != nil {
		t.Fatalf("git diff --commit: %v", err)
	}
	var doc struct {
		Root   string `json:"root"`
		Commit string `json:"commit"`
		Base   string `json:"base"`
		Files  []struct {
			Path string `json:"path"`
			Lines []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"lines"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not commit diff JSON: %v\n%s", err, out)
	}
	if doc.Root != dir || doc.Commit != strings.TrimSpace(commitOut) || doc.Base == "" {
		t.Fatalf("identity = %+v, want root/commit/base", doc)
	}
	if len(doc.Files) != 1 || doc.Files[0].Path != "kept.txt" {
		t.Fatalf("files = %+v", doc.Files)
	}
}


func TestGitDiffCommitJSONRootUsesEmptyBase(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "config", "user.email", "test@example.com")
	gitIn(t, dir, "config", "user.name", "Test")
	writeIn(t, dir, "root.txt", "root\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-qm", "root")
	commitOut := strings.TrimSpace(gitInOutput(t, dir, "rev-parse", "HEAD"))
	chdir(t, dir)

	prevJSON, prevFile, prevCommit := gitDiffJSONFlag, gitDiffFileFlag, gitDiffCommitFlag
	gitDiffJSONFlag, gitDiffFileFlag, gitDiffCommitFlag = true, "", commitOut
	t.Cleanup(func() { gitDiffJSONFlag, gitDiffFileFlag, gitDiffCommitFlag = prevJSON, prevFile, prevCommit })

	out, err := runGit(t, gitDiffCmd, gitDiffRun)
	if err != nil {
		t.Fatalf("git diff --commit root: %v", err)
	}
	var doc struct {
		Commit string `json:"commit"`
		Base   string `json:"base"`
		Files  []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not root commit diff JSON: %v\n%s", err, out)
	}
	if doc.Commit != commitOut || doc.Base != "" || len(doc.Files) != 1 || doc.Files[0].Path != "root.txt" {
		t.Fatalf("root identity = %+v, want commit, empty base, and root.txt", doc)
	}
}

func TestGitDiffCommitRejectsRefLikeIDAsUsage(t *testing.T) {
	dir := gitTestRepo(t)
	chdir(t, dir)
	prevCommit := gitDiffCommitFlag
	gitDiffCommitFlag = "refs/heads/main"
	t.Cleanup(func() { gitDiffCommitFlag = prevCommit })

	_, err := runGit(t, gitDiffCmd, gitDiffRun)
	var usage usageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want usageError", err)
	}
}

// A file the repository has, with nothing moved: not an empty answer, an answer
// with a reason. It exits 1 either way — a caller that asked for a patch and
// got nothing would otherwise wait for output that is never coming.
func TestGitDiffSaysWhyWhenThereIsNothingToShow(t *testing.T) {
	dir := gitTestRepo(t)
	chdir(t, dir)

	prevFile := gitDiffFileFlag
	t.Cleanup(func() { gitDiffFileFlag = prevFile })

	cases := []struct {
		name string
		file string
		want string
	}{
		{"unchanged file", "clean.txt", "has no changes to show"},
		{"no such path", "nope.txt", "is not in that repository"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gitDiffFileFlag = tc.file
			err := gitDiffRun(gitDiffCmd, nil)
			var ce *cliErr
			if !errors.As(err, &ce) || ce.Code != "not_found" {
				t.Fatalf("err = %v, want a not_found cliErr", err)
			}
			if !strings.Contains(ce.Msg, tc.want) {
				t.Errorf("message = %q, want it to say %q", ce.Msg, tc.want)
			}
			if ce.Hint == "" {
				t.Error("no hint: this is the case where a caller needs one")
			}
		})
	}
}

// Outside a checkout both commands say what the situation is, with the code the
// contract promises for it.
func TestGitCommandsOutsideARepository(t *testing.T) {
	chdir(t, t.TempDir())

	prev := gitFilesJSONFlag
	gitFilesJSONFlag = false
	t.Cleanup(func() { gitFilesJSONFlag = prev })

	for name, run := range map[string]func() error{
		"files": func() error { _, err := runGit(t, gitFilesCmd, gitFilesRun); return err },
		"diff":  func() error { _, err := runGit(t, gitDiffCmd, gitDiffRun); return err },
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			var ce *cliErr
			if !errors.As(err, &ce) || ce.Code != "not_found" {
				t.Fatalf("err = %v, want a not_found cliErr", err)
			}
			if !strings.Contains(ce.Msg, "not in a git repository") {
				t.Errorf("message = %q", ce.Msg)
			}
		})
	}
}

// gitTestRepo is the smallest repository with a row on each side: one file
// changed, one file left alone, one untracked.
func gitTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "config", "user.email", "test@example.com")
	gitIn(t, dir, "config", "user.name", "Test")
	writeIn(t, dir, "kept.txt", "one\ntwo\nthree\n")
	writeIn(t, dir, "clean.txt", "left alone\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-qm", "first")

	writeIn(t, dir, "kept.txt", "one\nTWO\nthree\nfour\n")
	writeIn(t, dir, "fresh.txt", "new\nfile\n")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitInOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeIn(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
