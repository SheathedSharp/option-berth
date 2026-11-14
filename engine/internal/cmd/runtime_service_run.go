package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/userenv"
	"github.com/spf13/cobra"
)

// detachedEnv marks the child `oberth runtime service run` that `runtime service run --detach` forked. The
// child is not a different command, only a differently plumbed one.
const detachedEnv = "BERTH_RUNTIME_SERVICE_DETACHED"

var runtimeServiceDetach bool

var runtimeServiceRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the oberth runtime service",
	Long: "Run the runtime service that owns port scanning. Every interface (CLI, tray,\n" +
		"desktop app) reads cached state and deltas from it instead\n" +
		"of scanning on its own.\n\n" +
		"Foreground by default, logging to stderr and to the log file.\n" +
		"`oberth runtime service path` prints the socket it listens on.",
	Args: cobra.NoArgs,
	RunE: runtimeServiceRun,
}

func init() {
	flags := runtimeServiceRunCmd.Flags()
	flags.BoolVarP(&runtimeServiceDetach, "detach", "d", false,
		"Start the runtime service in the background and return once it accepts connections")
	runtimeServiceCmd.AddCommand(runtimeServiceRunCmd)
}

func runtimeServiceRun(command *cobra.Command, _ []string) error {
	if err := refuseTestBinaryRuntimeService(); err != nil {
		return err
	}

	if runtimeServiceDetach {
		// The parent only forks; the process that becomes the runtime service is the
		// child, and it is the one that asks for the PATH below.
		return detachRuntimeService(command.Context(), runtimesvc.SocketPath())
	}
	return runForegroundRuntimeService(command.Context())
}

// runForegroundRuntimeService is the process that becomes the runtime.service. It owns the
// machine-wide view rather than a project, so its working directory and PATH
// must not be the checkout that happened to start it.
func runForegroundRuntimeService(ctx context.Context) error {
	if err := enterRuntimeServiceWorkDir(); err != nil {
		return err
	}

	// A service's environment is this process's, so this is the one place the
	// toolchain has to be right. Started from the desktop, the runtime service holds
	// launchd's four directories and nothing else — and then `pnpm run dev`
	// comes back "command not found" inside the program whose job is to start
	// it. See internal/userenv.
	loginPath, pathChanged := userenv.Ensure(userenv.Timeout)

	logger, closeLog, err := openRuntimeServiceLogger()
	if err != nil {
		return err
	}
	defer closeLog.Close()

	if pathChanged {
		logger.Info("PATH taken from the login shell", "path", loginPath)
	}

	srv := runtimesvc.New(runtimesvc.Options{
		Socket:                  runtimesvc.SocketPath(),
		PreviousServiceLockDirs: loadedConfig.RuntimeService.PreviousServiceLockDirs,
		Version:                 buildinfo.Version,
		IdleTimeout:             loadedConfig.RuntimeService.ResolvedIdleTimeout(),
		StatsInterval:           loadedConfig.RuntimeService.ResolvedStatsInterval(),
		ScanInterval:            loadedConfig.RuntimeService.ResolvedScanInterval(),
		Logger:                  logger,
	})

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := srv.Serve(ctx)
	switch {
	case serveErr == nil:
		return nil
	case runtimesvc.IsAlreadyRunning(serveErr):
		// Another runtime service already does the job; this is a refusal, not a crash.
		fmt.Fprintln(os.Stderr, "error:", serveErr)
		fmt.Fprintf(os.Stderr, "hint: `oberth runtime service status` shows it, `oberth runtime service restart` replaces it\n")
		os.Exit(1)
	}
	return serveErr
}

// enterRuntimeServiceWorkDir moves the runtime service into option-berth's own state directory.
//
// The runtime service holds a working directory, not a project: it watches every
// checkout on the machine, and every service it starts gets an explicit cwd
// from its manifest. Inheriting the caller's directory made the runtime service's own
// cwd part of port attribution and left it pointing at a deleted worktree once
// that checkout went away.
func enterRuntimeServiceWorkDir() error {
	workDir, err := runtimeServiceWorkDir()
	if err != nil {
		return err
	}
	if err := os.Chdir(workDir); err != nil {
		return fmt.Errorf("moving the runtime service to %s: %w", workDir, err)
	}
	return nil
}

// openRuntimeServiceLogger opens the runtime service's log at its configured level. A detached
// child's stderr is already redirected into that same file, so mirroring the
// logger there too would double every line.
func openRuntimeServiceLogger() (*slog.Logger, io.Closer, error) {
	alsoStderr := os.Getenv(detachedEnv) == ""
	return runtimesvc.NewLogger(runtimesvc.LogPath(),
		runtimesvc.ParseLogLevel(loadedConfig.RuntimeService.ResolvedLogLevel()), alsoStderr)
}

// refuseTestBinaryRuntimeService keeps a Go test binary from turning into the runtime.service.
//
// `go test` links a package into <pkg>.test, and Go's flag parser stops at the
// first non-flag argument — so `cmd.test serve --detach` silently ignores both
// words and runs the entire test suite in the background against the config
// directory it inherited. A process like that is a stray test run holding the
// user's real ~/.oberth open, not a runtime.service. Both entry points are covered:
// `runtime service run` refuses here, and `runtime service run --detach` re-execs this same binary, so it
// never gets far enough to fork.
func refuseTestBinaryRuntimeService() error {
	self, err := os.Executable()
	if err != nil || !runtimesvc.IsTestBinary(self) || runtimesvc.TestRuntimeServiceAllowed() {
		return nil
	}
	return fmt.Errorf("refusing to run %s as the oberth runtime service: it is a Go test binary, so this would re-run the test suite rather than serve\nhint: start a runtime service in-process from the test, or set %s=1 if you really mean it",
		filepath.Base(self), runtimesvc.AllowTestRuntimeServiceEnv)
}

// detachRuntimeService re-execs this binary as a background `oberth runtime service run` and waits for
// the socket to come up, so the caller can rely on the runtime service being ready when
// the command returns.
func detachRuntimeService(ctx context.Context, socket string) error {
	if err := alreadyRunningError(socket); err != nil {
		return err
	}
	child, logFile, err := detachedChild()
	if err != nil {
		return err
	}
	defer logFile.Close()

	pid, err := startRuntimeServiceChild(child)
	if err != nil {
		return err
	}
	if err := client.WaitForSocket(ctx, socket, client.AutostartTimeout); err != nil {
		return fmt.Errorf("%w (see %s)", err, runtimesvc.LogPath())
	}
	fmt.Printf("oberth runtime service started (pid %d), listening on %s\n", pid, socket)
	return nil
}

// detachedChild is the process that becomes the runtime_service: this binary re-executed
// as `runtime service run`, detached from this shell, running in the state directory and with
// the runtime service log as its only output — so a panic before its logger is up is
// still recoverable.
func detachedChild() (*exec.Cmd, *os.File, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("locating the option-berth binary: %w", err)
	}
	logFile, err := openRuntimeServiceLog()
	if err != nil {
		return nil, nil, err
	}
	// The child re-checks this, but setting it here means the runtime service never
	// starts out inside the checkout that happened to autostart it.
	workDir, err := runtimeServiceWorkDir()
	if err != nil {
		logFile.Close()
		return nil, nil, err
	}

	proc := exec.Command(binary, "runtime", "service", "run")
	proc.Env = append(os.Environ(), detachedEnv+"=1")
	proc.Stdin = nil
	proc.Stdout = logFile
	proc.Stderr = logFile
	proc.Dir = workDir
	detachProcess(proc)
	return proc, logFile, nil
}

// startRuntimeServiceChild launches the detached child and releases it: the runtime service has
// to outlive this process, so nothing here waits for it. The pid is the only
// thing the caller can report about a process it deliberately does not reap.
//
// A test replaces this to observe the launch without making a second test
// binary run the suite as a runtime.service.
var startRuntimeServiceChild = func(child *exec.Cmd) (int, error) {
	if err := child.Start(); err != nil {
		return 0, fmt.Errorf("starting the runtime_service: %w", err)
	}
	go func() { _ = child.Wait() }()
	return child.Process.Pid, nil
}

// alreadyRunningError is the blocked-start decision behind `--detach`: a live
// socket means another runtime service already does the job, and the lock holder's pid
// is part of the answer when it can be read. A free socket returns nil.
func alreadyRunningError(socket string) error {
	if !runtimesvc.SocketAlive(socket) {
		return nil
	}
	if pid := runtimesvc.LockHolderPID(runtimesvc.LockPath()); pid > 0 {
		return fmt.Errorf("runtime service already running (pid %d)", pid)
	}
	return fmt.Errorf("runtime service already running")
}

// openRuntimeServiceLog prepares the log the detached child's stdout and stderr go to.
// The directory is created owner-only, the same mode the state directory uses.
func openRuntimeServiceLog() (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(runtimesvc.LogPath()), 0o700); err != nil {
		return nil, fmt.Errorf("creating the log directory: %w", err)
	}
	f, err := os.OpenFile(runtimesvc.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", runtimesvc.LogPath(), err)
	}
	return f, nil
}

// runtimeServiceWorkDir is where a runtime service process runs: option-berth's own state
// directory, never the checkout that happened to start it. The directory is
// created owner-only, the same mode the rest of the state directory uses.
func runtimeServiceWorkDir() (string, error) {
	dir, err := paths.EnsureDir()
	if err != nil {
		return "", fmt.Errorf("preparing the runtime service working directory: %w", err)
	}
	return dir, nil
}

// stopTimeout bounds how long `runtime service stop` and `runtime service restart` wait for the
// previous runtime service to release the socket and the lock.
const stopTimeout = 10 * time.Second

// waitForSocketGone is the inverse of WaitForSocket, used by `runtime service restart`.
func waitForSocketGone(socket string, timeout time.Duration) {
	giveUp := time.Now().Add(timeout)
	for {
		if !runtimesvc.SocketAlive(socket) {
			return
		}
		if !time.Now().Before(giveUp) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// waitForRuntimeServiceGone waits until the previous runtime service has both stopped accepting
// connections and released the single-instance lock. The socket closes first,
// the lock goes last, so a restart that waited only for the socket raced the
// old runtime service's teardown: the replacement started, lost the lock, and exited
// "already running", leaving nothing running.
func waitForRuntimeServiceGone(socket string, timeout time.Duration) error {
	giveUp := time.Now().Add(timeout)
	waitForSocketGone(socket, timeout)

	left := time.Until(giveUp)
	if left < time.Second {
		left = time.Second
	}
	if err := runtimesvc.WaitForLockRelease(runtimesvc.LockPath(), left); err != nil {
		return fmt.Errorf("the running runtime service did not shut down: %w\nhint: `oberth runtime service status` shows it; kill it and retry", err)
	}
	return nil
}
