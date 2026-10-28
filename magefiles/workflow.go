//go:build mage

package main

import (
	"strings"
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

func renderClient(flag, filename string, args ...string) error { return clientMoved() }

func openInstalledClient(root string) error {
	return clientMoved()
}
