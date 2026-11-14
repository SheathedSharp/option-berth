package runtimesvc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// AcquireServicePort reads the listener table to avoid handing out a bound
// port. That read is a full OS scan, and the claims lock must not span it:
// a concurrent `down` captures its release plan with TryLock and would report
// "claims are changing; retry down" for as long as the scan runs. The scan
// itself is also the slowest thing on this path, so holding the runtime service-wide
// claims lock across it is exactly the "no lock spans IO" boundary.
func TestAcquireServicePortDoesNotHoldClaimsLockAcrossTheScan(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rt := &HostContext{Store: st}
	srv := &Server{conns: map[uint64]*Conn{}}
	rt.srv = srv
	rt.Scanner = scanner.New(scanner.Options{
		Runs: rt.RunRegistry,
		ScanContext: func(context.Context, scanner.Include) ([]ports.ListeningPort, error) {
			if !claimsMu.TryLock() {
				t.Error("the listener scan ran while the claims lock was held")
				return nil, nil
			}
			claimsMu.Unlock()
			return nil, nil
		},
	})

	if _, err := AcquireServicePort(rt, dir, "web"); err != nil {
		t.Fatal(err)
	}
}
