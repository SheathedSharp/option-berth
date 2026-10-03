package ports

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type collectorExit int

func (e collectorExit) Error() string { return "collector exit" }
func (e collectorExit) ExitCode() int { return int(e) }

func TestListenerCommandFailureIsNotAnEmptyHealthyScan(t *testing.T) {
	valid := darwinTCPHeader + "tcp4 0 0 127.0.0.1.8080 *.* LISTEN 0 0 128 128 node:42 0 0\n"
	cases := []struct {
		name, platform, out, stderr string
		err                         error
		wantError                   bool
		rows                        int
	}{
		{"empty success", "darwin", darwinTCPHeader, "", nil, false, 0},
		{"no table", "darwin", "", "", nil, true, 0},
		{"missing tool", "darwin", "", "", exec.ErrNotFound, true, 0},
		{"permission failure", "darwin", "", "permission denied", collectorExit(1), true, 0},
		{"partial evidence", "darwin", valid, "some processes inaccessible", collectorExit(1), true, 0},
		{"successful exit with warning", "darwin", valid, "incomplete output", nil, true, 0},
		{"usage failure with prefix", "darwin", valid, "failure", collectorExit(2), true, 0},
		{"interrupted with prefix", "darwin", valid, "", collectorExit(-1), true, 0},
		{"canceled", "darwin", valid, "", context.Canceled, true, 0},
		{"ss empty failure", "linux", "", "", collectorExit(1), true, 0},
		{"netstat failure", "windows", "", "denied", collectorExit(1), true, 0},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := collectListeners(tt.platform, func(string, ...string) ([]byte, []byte, error) {
				return []byte(tt.out), []byte(tt.stderr), tt.err
			})
			if (err != nil) != tt.wantError || len(got) != tt.rows {
				t.Fatalf("rows=%d err=%v", len(got), err)
			}
			if tt.wantError && tt.err != nil && !errors.Is(err, tt.err) {
				t.Fatalf("lost error cause: %v", err)
			}
			if tt.wantError && tt.stderr != "" && !strings.Contains(err.Error(), tt.stderr) {
				t.Fatal("lost diagnostic")
			}
		})
	}
}

func TestListenerCommandUsesOnlyFixedPlatformArguments(t *testing.T) {
	for _, tt := range []struct {
		platform, tool string
		args           []string
	}{
		{"darwin", "netstat", []string{"-anv", "-p", "tcp"}},
		{"linux", "ss", []string{"-tlnp"}},
		{"windows", "netstat", []string{"-ano"}},
	} {
		calls := 0
		_, err := collectListeners(tt.platform, func(tool string, args ...string) ([]byte, []byte, error) {
			calls++
			if tool != tt.tool || !reflect.DeepEqual(args, tt.args) {
				t.Fatalf("command = %s %v", tool, args)
			}
			if tt.platform == "darwin" {
				return []byte(darwinTCPHeader), nil, nil
			}
			return nil, nil, nil
		})
		if err != nil || calls != 1 {
			t.Fatalf("calls=%d error=%v", calls, err)
		}
	}
	_, err := collectListeners("unsupported", func(string, ...string) ([]byte, []byte, error) {
		t.Fatal("unsupported platform invoked a command")
		return nil, nil, nil
	})
	if err == nil {
		t.Fatal("missing unsupported-platform error")
	}
}

func TestScanFindsIsolatedListener(t *testing.T) {
	tool := map[string]string{"darwin": "netstat", "linux": "ss", "windows": "netstat"}[runtime.GOOS]
	if tool == "" {
		t.Skip("unsupported native platform")
	}
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("native collector unavailable: %v", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	rows, err := Scan()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Port == port && row.PID == os.Getpid() && row.BindAddress == "127.0.0.1" {
			return
		}
	}
	t.Fatalf("native capture missed isolated listener pid=%d port=%d", os.Getpid(), port)
}
