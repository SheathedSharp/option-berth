package git

import (
	"context"
	"testing"
)

func TestDiffCommitTruncatesLargeFileList(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	for i := 0; i < maxCommitDiffFiles+5; i++ {
		write(t, dir, "file-"+itoa(i)+".txt", "body\n")
	}
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "many files")
	commit := head(t, dir)

	patch, err := DiffCommit(context.Background(), dir, commit, "")
	if err != nil {
		t.Fatalf("DiffCommit: %v", err)
	}
	if !patch.Truncated || patch.TotalFiles != maxCommitDiffFiles+5 {
		t.Fatalf("truncation = %v total=%d, want truncated with %d files",
			patch.Truncated, patch.TotalFiles, maxCommitDiffFiles+5)
	}
	if len(patch.Files) != maxCommitDiffFiles {
		t.Fatalf("files = %d, want cap %d", len(patch.Files), maxCommitDiffFiles)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
