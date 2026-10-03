//go:build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Engine builds only the CLI/daemon; the canonical BuildEngine name remains.
func Engine() error { return BuildEngine() }

// Jev builds only the optional attention adapter.
func Jev() error { return BuildJev() }

// Client builds only the macOS client.
func Client() error { return BuildClient() }

// Run builds and installs the products, then opens the installed application.
func Run() error {
	if err := Install(); err != nil {
		return err
	}
	p, err := loadProject()
	if err != nil {
		return err
	}
	return openInstalledClient(p.root)
}

// Fmt formats Go source in the engine without installing or running products.
func Fmt() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return command(p.engine, "gofmt", "-w", ".")
}

// Schema regenerates the existing protocol schema.
func Schema() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return command(p.engine, "go", "generate", "./internal/daemon/rpc")
}

// Shot renders current state, optionally limited by -scope=services:NAME.
func Shot(scope *string) error {
	if scope == nil || *scope == "" {
		return renderClient("--snapshot", "board.png")
	}
	return renderClient("--snapshot", snapshotFile(*scope), "--scope", *scope)
}

func snapshotFile(scope string) string {
	// A scope is UI input, never a filesystem path outside the cache.
	name := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(scope)
	return "scope-" + name + ".png"
}

func renderClient(flag, filename string, args ...string) error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	binary := clientBinary(p)
	if !fileExists(binary) {
		return fmt.Errorf("client is not built; run mage client or mage buildClient first")
	}
	out := filepath.Join(p.root, "client", "macos", ".cache", filename)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return command(p.root, binary, append([]string{flag, out}, args...)...)
}

func openInstalledClient(root string) error {
	// `open` otherwise reuses an older running binary with the same bundle ID.
	// Request a normal quit, never kill arbitrary processes matching a name.
	script := `with timeout of 5 seconds
if application id "dev.option-berth.app" is running then tell application id "dev.option-berth.app" to quit
end timeout`
	if err := command(root, "osascript", "-e", script); err != nil {
		return err
	}
	for i := 0; i < 20; i++ {
		output, err := exec.Command("osascript", "-e", `application id "dev.option-berth.app" is running`).Output()
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(output)) == "false" {
			return command(root, "open", "/Applications/OptionBerth.app")
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("installed client did not quit; close it before running the new build")
}
