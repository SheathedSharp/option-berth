//go:build mage

// The development workflow is intentionally a Magefile rather than a second
// command line application.  Product users run oberth; contributors run mage.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const modulePath = "github.com/sheathedsharp/option-berth"

var semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Default builds every repository artifact.
var Default = Build

type project struct {
	root    string
	engine  string
	version string
	commit  string
	date    string
}

// Build compiles the engine, optional Jev adapter, and macOS client.
func Build() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := buildEngine(p); err != nil {
		return err
	}
	if err := buildJev(p); err != nil {
		return err
	}
	return buildClient(p)
}

// BuildEngine compiles the Go CLI and daemon.
func BuildEngine() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return buildEngine(p)
}

// BuildJev compiles the optional Jev attention adapter.
func BuildJev() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return buildJev(p)
}

// BuildClient compiles the macOS application.
func BuildClient() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return buildClient(p)
}

// RunCLI builds and runs the CLI. Pass one quoted argument string, for example
// `mage runCLI "version --json"`.
func RunCLI(args string) error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := buildEngine(p); err != nil {
		return err
	}
	return commandWithIO(p.root, filepath.Join(p.root, "bin", "oberth"), splitArgs(args)...)
}

// RunDaemon builds the CLI and starts the background collector.
func RunDaemon() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := buildEngine(p); err != nil {
		return err
	}
	return commandWithIO(p.root, filepath.Join(p.root, "bin", "oberth"), "serve", "--detach")
}

// RunApp builds, installs, and opens the macOS application.
func RunApp() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := installApp(p); err != nil {
		return err
	}
	return command(p.root, "open", "/Applications/OptionBerth.app")
}

// Test runs the Go test suite.
func Test() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return command(p.engine, "go", "test", "./...")
}

// Vet runs the Go compiler and vet checks used by releases.
func Vet() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return command(p.engine, "go", "vet", "./...")
}

// Version prints the repository version and build identity.
func Version() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	fmt.Printf("version=v%s\ntag=v%s\ncommit=%s\nbuild_number=%s\n", p.version, p.version, p.commit, buildNumber(p.root))
	return nil
}

// Install builds and installs the CLI, adapter, and macOS application.
func Install() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := installCLI(p); err != nil {
		return err
	}
	return installApp(p)
}

// InstallCLI builds the CLI and adapter, then configures ~/.local/bin on PATH.
func InstallCLI() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return installCLI(p)
}

// InstallApp builds and installs the macOS application.
func InstallApp() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return installApp(p)
}

// Stop stops the daemon belonging to this checkout.
func Stop() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	bin := filepath.Join(p.root, "bin", "oberth")
	if !fileExists(bin) {
		bin = filepath.Join(home, ".local", "bin", "oberth")
	}
	if !fileExists(bin) {
		return errors.New("oberth is not built or installed; run mage install or mage buildEngine first")
	}
	return commandWithIO(p.root, bin, "daemon", "stop")
}

// Clean removes generated binaries, app bundles, and image snapshots.
func Clean() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	for _, path := range []string{
		filepath.Join(p.root, "bin"),
		filepath.Join(p.root, "client", "macos", "build"),
		filepath.Join(p.root, "client", "macos", ".cache"),
	} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot renders the current daemon state to a PNG.
func Snapshot() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := buildClient(p); err != nil {
		return err
	}
	app := clientBinary(p)
	out := filepath.Join(p.root, "client", "macos", ".cache", "board.png")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return command(p.root, app, "--snapshot", out)
}

// States renders the deterministic client state gallery.
func States() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := buildClient(p); err != nil {
		return err
	}
	out := filepath.Join(p.root, "client", "macos", ".cache", "states")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return command(p.root, clientBinary(p), "--render-states", out, "--size", "900x640")
}

// Window renders the client window chrome to a PNG.
func Window() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := buildClient(p); err != nil {
		return err
	}
	out := filepath.Join(p.root, "client", "macos", ".cache", "window.png")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return command(p.root, clientBinary(p), "--dump-window", out)
}

// Service renders one project's service view. Pass the project name as the argument.
func Service(projectName string) error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := buildClient(p); err != nil {
		return err
	}
	out := filepath.Join(p.root, "client", "macos", ".cache", "services-"+projectName+".png")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return command(p.root, clientBinary(p), "--snapshot", out, "--scope", "services:"+projectName)
}

// ReleasePatch increments PATCH, commits VERSION, and creates an annotated tag.
func ReleasePatch(dryRun *bool) error {
	return release("patch", dryRun != nil && *dryRun)
}

// ReleaseMinor increments MINOR, commits VERSION, and creates an annotated tag.
func ReleaseMinor(dryRun *bool) error {
	return release("minor", dryRun != nil && *dryRun)
}

// ReleaseMajor increments MAJOR, commits VERSION, and creates an annotated tag.
func ReleaseMajor(dryRun *bool) error {
	return release("major", dryRun != nil && *dryRun)
}

func loadProject() (project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return project{}, err
	}
	root := cwd
	if filepath.Base(root) == "magefiles" {
		root = filepath.Dir(root)
	}
	for {
		if fileExists(filepath.Join(root, "VERSION")) && fileExists(filepath.Join(root, "engine", "go.mod")) {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return project{}, errors.New("run mage from the option-berth repository")
		}
		root = parent
	}
	versionBytes, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return project{}, err
	}
	version := strings.TrimSpace(strings.TrimPrefix(string(versionBytes), "v"))
	if !semverPattern.MatchString(version) {
		return project{}, fmt.Errorf("invalid VERSION %q; expected MAJOR.MINOR.PATCH", version)
	}
	commit, err := gitOutput(root, "rev-parse", "--short", "HEAD")
	if err != nil {
		commit = "local"
	}
	return project{
		root: root, engine: filepath.Join(root, "engine"), version: version,
		commit: commit, date: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func buildEngine(p project) error {
	bin := filepath.Join(p.root, "bin", "oberth")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	ldflags := strings.Join([]string{
		"-s", "-w",
		"-X", modulePath + "/internal/buildinfo.Version=v" + p.version,
		"-X", modulePath + "/internal/buildinfo.Commit=" + p.commit,
		"-X", modulePath + "/internal/buildinfo.Date=" + p.date,
	}, " ")
	if err := command(p.engine, "go", "build", "-trimpath", "-ldflags", ldflags, "-o", bin, "."); err != nil {
		return err
	}
	fmt.Printf("built %s (v%s, %s)\n", bin, p.version, p.commit)
	return nil
}

func buildJev(p project) error {
	bin := filepath.Join(p.root, "bin", "jev-attention")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	if err := command(p.engine, "go", "build", "-trimpath", "-o", bin, "./cmd/jev-attention"); err != nil {
		return err
	}
	fmt.Printf("built %s\n", bin)
	return nil
}

func buildClient(p project) error {
	cmd := exec.Command(filepath.Join(p.root, "client", "macos", "build.sh"))
	cmd.Dir = p.root
	cmd.Env = append(os.Environ(), "VERSION="+p.version, "BUILD_NUMBER="+buildNumber(p.root))
	return runCommand(cmd)
}

func installCLI(p project) error {
	if err := buildEngine(p); err != nil {
		return err
	}
	if err := buildJev(p); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		return err
	}
	if err := command(p.root, "install", "-m", "0755", filepath.Join(p.root, "bin", "oberth"), filepath.Join(localBin, "oberth")); err != nil {
		return err
	}
	if err := command(p.root, "install", "-m", "0755", filepath.Join(p.root, "bin", "jev-attention"), filepath.Join(localBin, "jev-attention")); err != nil {
		return err
	}
	return command(p.root, "sh", filepath.Join(p.root, "scripts", "install-path.sh"), localBin)
}

func installApp(p project) error {
	if err := buildClient(p); err != nil {
		return err
	}
	app := filepath.Join(p.root, "client", "macos", "build", "OptionBerth.app")
	destination := "/Applications/OptionBerth.app"
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	if err := command(p.root, "ditto", app, destination); err != nil {
		return err
	}
	fmt.Printf("installed %s\n", destination)
	return nil
}

func clientBinary(p project) string {
	return filepath.Join(p.root, "client", "macos", "build", "OptionBerth.app", "Contents", "MacOS", "OptionBerth")
}

func release(level string, dryRun bool) error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if level != "major" && level != "minor" && level != "patch" {
		return fmt.Errorf("unknown release level %q", level)
	}
	if err := requireCleanTree(p.root); err != nil {
		return err
	}
	if err := command(p.engine, "go", "vet", "./..."); err != nil {
		return fmt.Errorf("release verification failed: %w", err)
	}
	next, err := bumpVersion(p.version, level)
	if err != nil {
		return err
	}
	tag := "v" + next
	if _, err := gitOutput(p.root, "rev-parse", "--verify", "refs/tags/"+tag); err == nil {
		return fmt.Errorf("tag %s already exists", tag)
	}
	message := "chore(release): " + tag
	if dryRun {
		fmt.Printf("would write VERSION=%s, commit %q, and create annotated tag %s\n", next, message, tag)
		return nil
	}

	versionPath := filepath.Join(p.root, "VERSION")
	old := []byte(p.version + "\n")
	if err := os.WriteFile(versionPath, []byte(next+"\n"), 0o644); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.WriteFile(versionPath, old, 0o644)
		}
	}()
	if err := command(p.root, "git", "add", "VERSION"); err != nil {
		return err
	}
	if err := command(p.root, "git", "commit", "-m", message); err != nil {
		return err
	}
	committed = true
	if err := command(p.root, "git", "tag", "-a", tag, "-m", "Release "+tag); err != nil {
		return fmt.Errorf("release commit created but tag failed: %w", err)
	}
	fmt.Printf("released %s (%s)\n", tag, message)
	return nil
}

func bumpVersion(version, level string) (string, error) {
	parts := strings.Split(version, ".")
	if len(parts) != 3 || !semverPattern.MatchString(version) {
		return "", fmt.Errorf("invalid version %q", version)
	}
	values := make([]int, 3)
	for i := range parts {
		value, err := strconv.Atoi(parts[i])
		if err != nil {
			return "", err
		}
		values[i] = value
	}
	switch level {
	case "major":
		values[0]++
		values[1] = 0
		values[2] = 0
	case "minor":
		values[1]++
		values[2] = 0
	case "patch":
		values[2]++
	default:
		return "", fmt.Errorf("unknown release level %q", level)
	}
	return fmt.Sprintf("%d.%d.%d", values[0], values[1], values[2]), nil
}

func requireCleanTree(root string) error {
	out, err := gitOutput(root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return errors.New("release requires a clean git worktree")
	}
	return nil
}

func buildNumber(root string) string {
	value, err := gitOutput(root, "rev-list", "--count", "HEAD")
	if err != nil || value == "" {
		return "0"
	}
	return value
}

func splitArgs(args string) []string {
	if strings.TrimSpace(args) == "" {
		return nil
	}
	return strings.Fields(args)
}

func gitOutput(root string, args ...string) (string, error) {
	return output(root, "git", args...)
}

func output(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	bytes, err := cmd.Output()
	return strings.TrimSpace(string(bytes)), err
}

func command(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return runCommand(cmd)
}

func commandWithIO(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func runCommand(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
