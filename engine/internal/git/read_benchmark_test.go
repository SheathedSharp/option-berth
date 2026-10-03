package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// This file uses only the existing ReadTree API so the identical fixture can
// run against the pinned pre-change revision. Timings include Git processes,
// not just parsing. Allocations cover Go only, not Git's child-process memory.
func BenchmarkGitReadTreeRound(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Fatal("real Git is required for this benchmark")
	}
	for _, workload := range []string{"clean", "untracked", "unborn", "tracked"} {
		b.Run(workload, func(b *testing.B) {
			dir := b.TempDir()
			benchGitReadCommand(b, dir, "init", "-q")
			benchGitReadCommand(b, dir, "config", "user.name", "Read benchmark")
			benchGitReadCommand(b, dir, "config", "user.email", "read@example.invalid")
			benchGitReadCommand(b, dir, "config", "core.autocrlf", "false")
			for i := 0; i < 32; i++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%02d.txt", i)), []byte("one\ntwo\n"), 0o600); err != nil {
					b.Fatal(err)
				}
			}
			if workload != "unborn" {
				benchGitReadCommand(b, dir, "add", ".")
				benchGitReadCommand(b, dir, "commit", "-qm", "fixture")
			}
			if workload == "untracked" {
				if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o600); err != nil {
					b.Fatal(err)
				}
			}
			if workload == "tracked" {
				if err := os.WriteFile(filepath.Join(dir, "file-00.txt"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
					b.Fatal(err)
				}
			}
			// Warm both versions and fail rather than benchmark an error path.
			if _, err := ReadTree(context.Background(), dir); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := ReadTree(context.Background(), dir); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchGitReadCommand(b *testing.B, dir string, args ...string) {
	b.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
