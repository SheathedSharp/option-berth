package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
)

// The exit codes docs/cli.md documents, and the classification behind them.
// Everything a caller branches on has to come out of here.
func TestExitCodesMatchTheContract(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"failure", errors.New("boom"), exitFail},
		{"usage", usageError{errors.New("unknown flag: --nope")}, exitUsage},
		{"wrapped usage", fmt.Errorf("list: %w", usageError{errors.New("bad arg")}), exitUsage},
		{"coded failure", fail("not_found", "no process on port 3000"), exitFail},
		{"silent", errSilent, exitFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Fatalf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestPublicCommandSurfaceHasOneProductRing(t *testing.T) {
	want := map[string]string{
		"status": commandGroupCore, "init": commandGroupCore, "up": commandGroupCore,
		"down": commandGroupCore, "restart": commandGroupCore, "logs": commandGroupCore,
		"events": commandGroupSupport, "git": commandGroupSupport, "doctor": commandGroupSupport,
		"start": commandGroupSupport, "kill": commandGroupSupport,
		"daemon": commandGroupInfra, "serve": commandGroupInfra,
		"config": commandGroupInfra, "version": commandGroupInfra,
		"completion": commandGroupInfra,
	}
	public := 0
	for _, command := range rootCmd.Commands() {
		if command.Hidden || command.Name() == "help" {
			continue
		}
		public++
		if got, ok := want[command.Name()]; !ok {
			t.Errorf("unexpected public root command %q", command.Name())
		} else if command.GroupID != got {
			t.Errorf("%s group = %q, want %q", command.Name(), command.GroupID, got)
		}
	}
	if public != len(want) {
		t.Fatalf("public command count = %d, want %d", public, len(want))
	}
}

func TestLowLevelCommandSurfacesStayNarrow(t *testing.T) {
	if startCmd.Use != "start [-d] [flags] -- <command> [args...]" {
		t.Fatalf("start usage = %q", startCmd.Use)
	}
	for _, name := range []string{"list", "json"} {
		if flag := startCmd.Flags().Lookup(name); flag != nil {
			t.Errorf("start --%s is a historical flag that should be removed", name)
		}
	}
	for _, name := range []string{"group", "session"} {
		if flag := killCmd.Flags().Lookup(name); flag == nil || !flag.Hidden {
			t.Errorf("kill --%s is part of the public command surface", name)
		}
	}
}

// A daemon error keeps the code the wire promised (contract §2): callers branch
// on error.data.code, so the CLI must not flatten it into "internal".
func TestDaemonErrorKeepsItsCode(t *testing.T) {
	const detail = "no process is listening on 127.0.0.1:3000"
	err := cliError(rpc.NewError(rpc.CodeNotFound, detail, "check the port"))

	var ce *cliErr
	if !errors.As(err, &ce) {
		t.Fatalf("err = %T, want *cliErr", err)
	}
	if ce.Code != "not_found" || ce.Msg != detail || ce.Hint != "check the port" {
		t.Fatalf("cliErr = %+v", ce)
	}
}

// In JSON mode the error is a document on stderr, so a caller can parse one
// stream without guessing which one it got.
func TestReportErrorWritesJSONToStderr(t *testing.T) {
	prev := jsonMode
	jsonMode = true
	t.Cleanup(func() { jsonMode = prev })

	var buf bytes.Buffer
	code := reportError(&buf, failHint("invalid_config", "line 4: port must be a number", "fix the file"))
	if code != exitFail {
		t.Fatalf("code = %d, want %d", code, exitFail)
	}

	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("stderr is not JSON: %v\n%s", err, buf.String())
	}
	if doc.Error.Code != "invalid_config" || doc.Error.Message != "line 4: port must be a number" {
		t.Fatalf("error document = %+v", doc.Error)
	}
	if doc.Error.Hint != "fix the file" {
		t.Fatalf("hint = %q", doc.Error.Hint)
	}
}

// The human rendering is the two lines the spec asks for: the detail, then the
// hint beneath it.
func TestReportErrorWritesDetailThenHint(t *testing.T) {
	prev := jsonMode
	jsonMode = false
	t.Cleanup(func() { jsonMode = prev })

	var buf bytes.Buffer
	reportError(&buf, failHint("not_found", "no process is listening", "run `oberth status`"))
	want := "error: no process is listening\nhint: run `oberth status`\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
}

// errSilent means "already reported" — non-zero, and quiet.
func TestErrSilentReportsNothing(t *testing.T) {
	var buf bytes.Buffer
	if code := reportError(&buf, errSilent); code != exitFail {
		t.Fatalf("code = %d, want %d", code, exitFail)
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %q, want nothing", buf.String())
	}
}

// A oberth.yaml at or above the directory names the project outright, and
// that name is what the daemon resolves ports to — so it is what the scope has
// to carry, not the directory name.
func TestProjectAtPrefersTheConfigName(t *testing.T) {
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "backend")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "name: my-project\nservices: []\n"
	if err := os.WriteFile(filepath.Join(dir, "oberth.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	name, root := projectAt(sub)
	if name != "my-project" || root != dir {
		t.Fatalf("projectAt = (%q, %q), want (my-project, %s)", name, root, dir)
	}
}

// `logs --json` is served by the daemon only: the direct path execs into tail,
// leaving no output of its own to structure. Saying so beats printing nothing.
func TestLogsJSONWithoutADaemonIsRefused(t *testing.T) {
	resetRouting(t)
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	rootCmd.SetArgs([]string{"logs", "3000", "--json"})

	err := rootCmd.Execute()
	var ce *cliErr
	if !errors.As(err, &ce) || ce.Code != "daemon_unavailable" {
		t.Fatalf("err = %v, want a daemon_unavailable cliErr", err)
	}
}

func TestInitPublicSurfaceIsScopedByAction(t *testing.T) {
	for _, name := range []string{"force", "merge", "diff", "service", "agent", "adopt-draft"} {
		flag := initCmd.Flags().Lookup(name)
		if flag != nil {
			t.Errorf("init --%s is a historical flag that should be removed", name)
		}
	}
	for _, name := range []string{"dry-run", "json"} {
		if flag := initCmd.Flags().Lookup(name); flag == nil || flag.Hidden {
			t.Errorf("init --%s is not public", name)
		}
	}
	if initDraftCmd.Flags().Lookup("agent") != nil {
		t.Error("init draft exposes agent selection as a flag; it should be positional")
	}
	for _, name := range []string{"replace", "merge", "dry-run", "diff", "json"} {
		if flag := initAdoptCmd.Flags().Lookup(name); flag == nil || flag.Hidden {
			t.Errorf("init adopt --%s is not public", name)
		}
	}
}
