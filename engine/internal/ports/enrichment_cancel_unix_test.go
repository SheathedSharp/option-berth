//go:build linux || darwin

package ports

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runOwnedMetadataCommand runs the real production adapter against one owned
// command. exec keeps the marker PID equal to the directly owned child. No
// detached helper is needed and a returned command must already be reaped.
func runOwnedMetadataCommand(t *testing.T, name string, call func(context.Context) error) error {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	script := "#!/bin/sh\nprintf '%s' \"$$\" > \"$OBERTH_METADATA_READY\"\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("OBERTH_METADATA_READY", ready)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- call(ctx) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Error("owned adapter failed to join during cleanup")
			}
		}
	}()
	var data []byte
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		data, _ = os.ReadFile(ready)
		if len(data) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("adapter never entered command: %q, %v", data, err)
	}
	cancel()
	select {
	case err = <-done:
		joined = true
	case <-time.After(2 * time.Second):
		t.Fatal("owned command ignored cancellation")
	}
	if e := syscall.Kill(pid, 0); e != syscall.ESRCH {
		t.Fatalf("command %d not reaped: %v", pid, e)
	}
	return err
}

func TestMetadataNativeCommandsCancelAndReap(t *testing.T) {
	names := []string{"process-table", "cim", "tasklist", "threads", "connections"}
	if runtime.GOOS == "darwin" {
		names = append(names, "cwd")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			executable := "ps"
			switch name {
			case "cim":
				executable = "powershell"
			case "tasklist":
				executable = "tasklist"
			case "cwd":
				executable = "lsof"
			case "connections":
				executable = "ss"
				if runtime.GOOS == "darwin" {
					executable = "lsof"
				}
			}
			// CIM/tasklist are command adapters under controlled Unix executables,
			// not a claim that Windows native CIM/PEB was exercised here.
			runOwnedMetadataCommand(t, executable, func(ctx context.Context) error {
				switch name {
				case "process-table":
					if got := batchGetProcessTableContext(ctx); len(got) != 0 {
						t.Error("cancelled table returned facts")
					}
				case "cim":
					if got := batchGetCommandsWindowsContext(ctx, []string{"42"}); len(got) != 0 {
						t.Error("cancelled CIM returned facts")
					}
				case "tasklist":
					rows := []ListeningPort{{PID: 42}}
					fillProcessNamesWindowsContext(ctx, rows, nil)
					if rows[0].Process != "" {
						t.Error("cancelled tasklist changed row")
					}
				case "threads":
					if got := countThreadsDarwinContext(ctx, []int{42}); len(got) != 0 {
						t.Error("cancelled threads returned facts")
					}
				case "connections":
					if got := connectionCountsContext(ctx); got != nil {
						t.Error("cancelled connections returned facts")
					}
				case "cwd":
					if got := batchGetCwdsContext(ctx, []int{42}); got != nil {
						t.Error("cancelled cwd returned facts")
					}
				}
				return nil
			})
		})
	}
}

func TestEnrichmentRoundCancelsAndReaps(t *testing.T) {
	isolateSignalCaches(t)
	rows := []ListeningPort{{PID: 42, Port: 8080, Command: "prior"}}
	err := runOwnedMetadataCommand(t, "ps", func(ctx context.Context) error { return EnrichContext(ctx, rows) })
	if !errors.Is(err, context.Canceled) || rows[0].Command != "prior" || scanParentTable.Load() != nil || !displaySignalCache.at.IsZero() {
		t.Fatalf("cancelled metadata committed: err=%v rows=%+v", err, rows)
	}
}

func TestFullStatsRoundCancelsAndReaps(t *testing.T) {
	for _, stage := range []string{"sample", "connections"} {
		t.Run(stage, func(t *testing.T) {
			name := "ps"
			row := ListeningPort{PID: 42, Port: 8080, MemoryRSS: 17, Connections: 9}
			if stage == "connections" {
				row.Type = PortTypeDocker
				name = "ss"
				if runtime.GOOS == "darwin" {
					name = "lsof"
				}
			}
			rows := []ListeningPort{row}
			err := runOwnedMetadataCommand(t, name, func(ctx context.Context) error { return EnrichStatsContext(ctx, rows, nil) })
			if !errors.Is(err, context.Canceled) || rows[0].MemoryRSS != 17 || rows[0].Connections != 9 {
				t.Fatalf("stage=%s err=%v rows=%+v", stage, err, rows)
			}
		})
	}
}

func TestEnrichmentHonorsEarlierParentDeadline(t *testing.T) {
	isolateSignalCaches(t)
	hungCommand(t, "ps")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := EnrichContext(ctx, []ListeningPort{{PID: 42, Port: 8080}})
	if !errors.Is(err, context.DeadlineExceeded) || !displaySignalCache.at.IsZero() || scanParentTable.Load() != nil {
		t.Fatalf("parent deadline was not applied: %v", err)
	}
}

func TestSuccessfulEmptyConnectionsClearObservation(t *testing.T) {
	name := "ss"
	if runtime.GOOS == "darwin" {
		name = "lsof"
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	rows := []ListeningPort{{PID: 42, Port: 8080, Connections: 7}}
	applyConnectionCounts(rows)
	if rows[0].Connections != 0 {
		t.Fatal("successful empty collection retained stale count")
	}
}

func TestLsofConnectionEmptyMatchIsNotFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		knownZero  bool
	}{
		{"empty success", "exit 0", true},
		{"empty match", "exit 1", true},
		{"warning", "printf denied >&2; exit 1", false},
		{"error", "exit 2", false},
		{"partial", "printf '%s\\n' 'node 42 user 3u IPv4 1 0t0 TCP 127.0.0.1:8080->127.0.0.1:50000 (ESTABLISHED)'; exit 1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "lsof"), []byte("#!/bin/sh\n"+tc.body+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			got := connectionCountsFor(context.Background(), "darwin")
			if (got != nil) != tc.knownZero || len(got) != 0 {
				t.Fatalf("known zero=%v counts=%v want=%v", got != nil, got, tc.knownZero)
			}
		})
	}
}
