package docker

import "github.com/sheathedsharp/option-berth/internal/ports"

// EnrichFromPS stamps pp from `docker ps --format psFormat` output, as a scan
// does from the watcher's cached list. It lets the external test package drive
// enrichment into the killer, which it cannot import from inside the package.
func EnrichFromPS(pp []ports.ListeningPort, out []byte) {
	enrichFrom(pp, parsePS(out))
}
