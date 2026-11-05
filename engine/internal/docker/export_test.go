package docker

import "github.com/sheathedsharp/option-berth/engine/internal/ports"

// EnrichFromPS stamps rows from `docker ps --format psFormat` output, the same
// way a scan stamps them from the watcher's cached list.
//
// It is exported only for the external test package: the killer tests need to
// drive enrichment without importing the unexported parse path.
func EnrichFromPS(rows []ports.ListeningPort, psOutput []byte) {
	enrichFrom(rows, parsePS(psOutput))
}
