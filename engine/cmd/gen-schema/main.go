// Command gen-schema writes the checked-in JSON Schemas to disk: the daemon
// protocol schema, generated from the Go types in internal/state and
// internal/daemon/rpc, and the oberth.yaml manifest schema, generated from
// internal/groups. It is driven by a //go:generate directive in
// internal/daemon/rpc/schema.go; CI fails when a checked-in file differs from
// what this command produces.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

func main() {
	out := flag.String("o", filepath.Join("docs", "schema", "protocol.schema.json"),
		"path of the protocol schema file to write")
	manifestOut := flag.String("manifest-o", "",
		"path of the manifest schema file to write (default: manifest.schema.json next to -o)")
	flag.Parse()

	manifest := *manifestOut
	if manifest == "" {
		manifest = filepath.Join(filepath.Dir(*out), "manifest.schema.json")
	}
	if err := run(*out, rpc.Marshal()); err != nil {
		fmt.Fprintln(os.Stderr, "gen-schema:", err)
		os.Exit(1)
	}
	if err := run(manifest, groups.MarshalManifestSchema()); err != nil {
		fmt.Fprintln(os.Stderr, "gen-schema:", err)
		os.Exit(1)
	}
}

func run(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
