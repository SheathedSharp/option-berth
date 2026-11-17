//go:build mage

package main

// Engine builds only the CLI/Runtime Service; the canonical BuildEngine name remains.
func Engine() error { return BuildEngine() }

// Fmt formats Go source in the engine without installing or running products.
func Fmt() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return command(p.engine, "gofmt", "-w", ".")
}

// Schema regenerates the checked-in protocol and manifest JSON Schemas.
func Schema() error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	return command(p.engine, "go", "generate", "./internal/runtimesvc/rpc")
}
