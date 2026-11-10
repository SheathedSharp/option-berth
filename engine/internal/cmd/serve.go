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
	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/userenv"
	"github.com/spf13/cobra"
)

// detachedEnv marks the child `oberth serve` that `serve --detach` forked. The
// child is not a different command, only a differently plumbed one.
const detachedEnv = "BERTH_DAEMON_DETACHED"

var serveDetach bool

var serveCmd = &cobra.Command{
	Use:     "serve",
	Short:   "Run the oberth daemon",
	GroupID: commandGroupInfra,
	Long: "Run the daemon that owns port scanning. Every interface (CLI, tray,\n" +
		"desktop app) reads cached state and deltas from it instead\n" +
		"of scanning on its own.\n\n" +
		"Foreground by default, logging to stderr and to the log file.\n" +
		"`oberth daemon path` prints the socket it listens on.",
	Args: cobra.NoArgs,
	RunE: serveRun,
}

func init() {
	serveCmd.Flags().BoolVarP(&serveDetach, "detach", "d", false,
		"Start the daemon in the background and return once it accepts connections")
	rootCmd.AddCommand(serveCmd)
}

func serveRun(cmd *cobra.Command, _ []string) error {
	if err := refuseTestBinaryDaemon(); err != nil {
		return err
	}

	if serveDetach {
		// The parent only forks; the process that becomes the daemon is the
		// child, and it is the one that asks for the PATH below.
		return detachDaemon(cmd.Context(), daemon.SocketPath())
	}
	return runForegroundDaemon(cmd.Context())
}

// runForegroundDaemon is the process that becomes the daemon. It owns the
// machine-wide view rather than a project, so its working directory and PATH
// must not be the checkout that happened to start it.
func runForegroundDaemon(ctx context.Context) error {
	if err := enterDaemonWorkDir(); err != nil {
		return err
	}

	// A service's environment is this process's, so this is the one place the
	// toolchain has to be right. Started from the desktop, the daemon holds
	// launchd's four directories and nothing else — and then `pnpm run dev`
	// comes back "command not found" inside the program whose job is to start
	// it. See internal/userenv.
	loginPath, pathChanged := userenv.Ensure(userenv.Timeout)

	logger, closeLog, err := openDaemonLogger()
	if err != nil {
		return err
	}
	defer closeLog.Close()

	if pathChanged {
		logger.Info("PATH taken from the login shell", "path", loginPath)
	}

	srv := daemon.New(daemon.Options{
		Socket:        daemon.SocketPath(),
		Version:       buildinfo.Version,
		IdleTimeout:   loadedConfig.Daemon.ResolvedIdleTimeout(),
		StatsInterval: loadedConfig.Daemon.ResolvedStatsInterval(),
		ScanInterval:  loadedConfig.Daemon.ResolvedScanInterval(),
		Logger:        logger,
	})

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx); err != nil {
		if daemon.IsAlreadyRunning(err) {
			// Not a crash: another daemon is doing the job.
			fmt.Fprintln(os.Stderr, "error:", err)
			fmt.Fprintf(os.Stderr, "hint: `oberth daemon status` shows it, `oberth daemon restart` replaces it\n")
			os.Exit(1)
		}
		return err
	}
	return nil
}

// enterDaemonWorkDir moves the daemon into option-berth's own state directory.
//
// The daemon holds a working directory, not a project: it watches every
// checkout on the machine, and every service it starts gets an explicit cwd
// from its manifest. Inheriting the caller's directory made the daemon's own
// cwd part of port attribution and left it pointing at a deleted worktree once
// that checkout went away.
func enterDaemonWorkDir() error {
	workDir, err := daemonWorkDir()
	if err != nil {
		return err
	}
	if err := os.Chdir(workDir); err != nil {
		return fmt.Errorf("moving the daemon to %s: %w", workDir, err)
	}
	return nil
}

// openDaemonLogger opens the daemon's log at its configured level. A detached
// child's stderr is already redirected into that same file, so mirroring the
// logger there too would double every line.
func openDaemonLogger() (*slog.Logger, io.Closer, error) {
	alsoStderr := os.Getenv(detachedEnv) == ""
	return daemon.NewLogger(daemon.LogPath(),
		daemon.ParseLogLevel(loadedConfig.Daemon.ResolvedLogLevel()), alsoStderr)
}

// refuseTestBinaryDaemon stops a Go test binary from becoming the daemon.
//
// `go test` links a package into <pkg>.test, and Go's flag parser stops at the
// first non-flag argument — so `cmd.test serve --detach` silently ignores both
// words and runs the entire test suite in the background against the config
// directory it inherited. Every such process is a stray test run holding the
// user's real ~/.oberth open, not a daemon. Both entry
// points are covered: `serve` refuses here, and `serve --detach` re-execs this
// same binary, so it never gets far enough to fork.
func refuseTestBinaryDaemon() error {
	exe, err := os.Executable()
	if err != nil || !daemon.IsTestBinary(exe) || daemon.TestDaemonAllowed() {
		return nil
	}
	return fmt.Errorf("refusing to run %s as the oberth daemon: it is a Go test binary, so this would re-run the test suite rather than serve\nhint: start a daemon in-process from the test, or set %s=1 if you really mean it",
		filepath.Base(exe), daemon.AllowTestDaemonEnv)
}

// detachDaemon re-execs this binary as a background `oberth serve` and waits for
// the socket to come up, so the caller can rely on the daemon being ready when
// the command returns.
func detachDaemon(ctx context.Context, socket string) error {
	if err := alreadyRunningError(socket); err != nil {
		return err
	}
	child, logFile, err := detachedChild()
	if err != nil {
		return err
	}
	defer logFile.Close()

	pid, err := startDaemonChild(child)
	if err != nil {
		return err
	}
	if err := client.WaitForSocket(ctx, socket, client.AutostartTimeout); err != nil {
		return fmt.Errorf("%w (see %s)", err, daemon.LogPath())
	}
	fmt.Printf("oberth daemon started (pid %d), listening on %s\n", pid, socket)
	return nil
}

// detachedChild is the process that becomes the daemon: this binary re-executed
// as `serve`, detached from this shell, running in the state directory and with
// the daemon log as its only output — so a panic before its logger is up is
// still recoverable.
func detachedChild() (*exec.Cmd, *os.File, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("locating the option-berth binary: %w", err)
	}
	logFile, err := openDaemonLog()
	if err != nil {
		return nil, nil, err
	}
	// The child re-checks this, but setting it here means the daemon never
	// starts out inside the checkout that happened to autostart it.
	workDir, err := daemonWorkDir()
	if err != nil {
		logFile.Close()
		return nil, nil, err
	}

	child := exec.Command(exe, "serve")
	child.Env = append(os.Environ(), detachedEnv+"=1")
	child.Stdin = nil
	child.Stdout = logFile
	child.Stderr = logFile
	child.Dir = workDir
	detachProcess(child)
	return child, logFile, nil
}

// startDaemonChild launches the detached child and releases it: the daemon has
// to outlive this process, so nothing here waits for it. The pid is the only
// thing the caller can report about a process it deliberately does not reap.
//
// A test replaces this to observe the launch without making a second test
// binary run the suite as a daemon.
var startDaemonChild = func(child *exec.Cmd) (int, error) {
	if err := child.Start(); err != nil {
		return 0, fmt.Errorf("starting the daemon: %w", err)
	}
	go func() { _ = child.Wait() }()
	return child.Process.Pid, nil
}

// alreadyRunningError is the blocked-start decision behind `--detach`: a live
// socket means another daemon already does the job, and the lock holder's pid
// is part of the answer when it can be read. A free socket returns nil.
func alreadyRunningError(socket string) error {
	if !daemon.SocketAlive(socket) {
		return nil
	}
	if pid := daemon.LockHolderPID(daemon.LockPath()); pid > 0 {
		return fmt.Errorf("daemon already running (pid %d)", pid)
	}
	return fmt.Errorf("daemon already running")
}

// openDaemonLog prepares the log the detached child's stdout and stderr go to.
// The directory is created owner-only, the same mode the state directory uses.
func openDaemonLog() (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(daemon.LogPath()), 0o700); err != nil {
		return nil, fmt.Errorf("creating the log directory: %w", err)
	}
	f, err := os.OpenFile(daemon.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", daemon.LogPath(), err)
	}
	return f, nil
}

// daemonWorkDir is where a daemon process runs: option-berth's own state
// directory, never the checkout that happened to start it. The directory is
// created owner-only, the same mode the rest of the state directory uses.
func daemonWorkDir() (string, error) {
	dir, err := paths.EnsureDir()
	if err != nil {
		return "", fmt.Errorf("preparing the daemon working directory: %w", err)
	}
	return dir, nil
}

// stopTimeout bounds how long `daemon stop` and `daemon restart` wait for the
// previous daemon to let go of the socket and the lock.
const stopTimeout = 10 * time.Second

// waitForSocketGone is the inverse of WaitForSocket, used by `daemon restart`.
func waitForSocketGone(socket string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !daemon.SocketAlive(socket) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// waitForDaemonGone waits until the previous daemon has both stopped accepting
// connections and released the single-instance lock. The socket closes first
// and the lock is released last, so a restart that only waited for the socket
// raced the old daemon's teardown: the replacement started, lost the lock, and
// exited "already running", leaving nothing running.
func waitForDaemonGone(socket string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	waitForSocketGone(socket, timeout)

	remaining := time.Until(deadline)
	if remaining < time.Second {
		remaining = time.Second
	}
	if err := daemon.WaitForLockRelease(daemon.LockPath(), remaining); err != nil {
		return fmt.Errorf("the running daemon did not shut down: %w\nhint: `oberth daemon status` shows it; kill it and retry", err)
	}
	return nil
}
