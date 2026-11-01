//go:build mage

// The development workflow is intentionally a Magefile rather than a second
// command line application.  Product users run oberth; contributors run mage.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const modulePath = "github.com/sheathedsharp/option-berth/engine"

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

// Build compiles the engine and the optional Jev adapter.
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
	return nil
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
// `mage runCLI "version --json"`. For exact argv (including spaces, empty
// arguments and backslashes), pass a JSON string array instead.
func RunCLI(args string) error {
	argv, err := splitArgs(args)
	if err != nil {
		return err
	}
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := buildEngine(p); err != nil {
		return err
	}
	return commandWithIO(p.root, filepath.Join(p.root, "bin", "oberth"), argv...)
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
	return openInstalledClient(p.root)
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

// Install builds and installs the CLI and adapter.
func Install() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return installCLI(p)
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
	} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot renders current daemon state with an already-built client.
func Snapshot() error { return Shot(nil) }

// States renders frozen UI fixtures. Rebuilds the macOS client so snapshot code stays current.
func States() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if err := buildClient(p); err != nil {
		return err
	}
	return renderClient("--render-states", "states", "--size", "900x640")
}

// Window captures the current client window without an implicit build.
func Window() error { return renderClient("--dump-window", "window.png") }

// Service is the compatible spelling of `mage shot -scope=services:NAME`.
func Service(name string) error { scope := "services:" + name; return Shot(&scope) }

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
	if err := command(p.engine, "go", "build", "-trimpath", "-ldflags", ldflags, "-o", bin, "./cmd/oberth"); err != nil {
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

// clientMoved names the product that now owns the macOS interface. Mage stays a
// single product's build tool rather than pretending to build a sibling repo.
func clientMoved() error {
	return fmt.Errorf("the macOS client is a separate product: https://github.com/SheathedSharp/option-berth-macos")
}

func buildClient(p project) error { return clientMoved() }

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

func splitArgs(args string) ([]string, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return nil, nil
	}
	if strings.ContainsRune(args, 0) {
		return nil, errors.New("CLI arguments cannot contain NUL")
	}
	if strings.HasPrefix(args, "[") {
		var values []any
		if err := json.Unmarshal([]byte(args), &values); err != nil {
			return nil, fmt.Errorf("CLI arguments must be a JSON array of strings: %w", err)
		}
		argv := make([]string, len(values))
		for i, value := range values {
			argument, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("CLI argument %d must be a JSON string", i)
			}
			if strings.ContainsRune(argument, 0) {
				return nil, fmt.Errorf("CLI argument %d contains NUL", i)
			}
			argv[i] = argument
		}
		return argv, nil
	}
	if strings.ContainsAny(args, "\"'\\") {
		return nil, errors.New("quoted or escaped CLI arguments require a JSON string array; for example [\"doctor\",\"--project\",\"project with spaces\"]")
	}
	return strings.Fields(args), nil
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
