package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// reviewAdoption renders exactly the bytes adoption would write, including a
// merge's preserved comments. --diff compares those bytes to the current file
// using private temporary files; the worktree and draft remain untouched.
func reviewAdoption(target, source, name string, data []byte, services, merged int, diff, jsonOutput bool) error {
	result := initResult{Path: target, Worktree: name, Services: services,
		Merged: merged, AdoptedFrom: source, YAML: string(data)}
	if diff {
		before, err := os.ReadFile(target)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		patch, err := adoptionDiff(before, data)
		if err != nil {
			return err
		}
		result.Diff = &patch
	}
	if jsonOutput {
		return printJSON(result)
	}
	if result.Diff != nil {
		if *result.Diff == "" {
			fmt.Println("no changes")
		} else {
			fmt.Print(*result.Diff)
		}
		return nil
	}
	fmt.Print(result.YAML)
	return nil
}

func adoptionDiff(before, after []byte) (string, error) {
	if bytes.Equal(before, after) {
		return "", nil
	}
	dir, err := os.MkdirTemp("", "berth-adoption-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	for name, contents := range map[string][]byte{"current.yaml": before, "draft.yaml": after} {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			return "", err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--no-pager", "diff", "--no-index", "--no-color",
		"--no-ext-diff", "--no-textconv", "--", "current.yaml", "draft.yaml")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fail("timeout", "adoption diff timed out")
	}
	var exit *exec.ExitError
	if err == nil || (errors.As(err, &exit) && exit.ExitCode() == 1) {
		return string(out), nil // git uses 1 for a successful comparison with changes
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", failHint("not_found", "git is not on PATH", "preview the YAML without --diff, or install git")
	}
	return "", fmt.Errorf("adoption diff: %w (%s)", err, bytes.TrimSpace(stderr.Bytes()))
}
