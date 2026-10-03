//go:build linux

package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var metadataFixtureIDs atomic.Int32

// This uses the default scanner and real command adapters, not Options.Scan.
// A unique synthetic PID avoids relying on package-global metadata cache resets.
func metadataFixture(t *testing.T) (ready, calls string) {
	t.Helper()
	dir := t.TempDir()
	ready = filepath.Join(dir, "ready")
	calls = filepath.Join(dir, "calls")
	pid := strconv.Itoa(2000000000 + int(metadataFixtureIDs.Add(1)))
	ss := "#!/bin/sh\nif [ \"$1\" != -tlnp ]; then printf x >> \"$OBERTH_METADATA_CONNECTIONS\"; exit 0; fi\nprintf '%s\\n' 'State Recv-Q Send-Q Local Address:Port Peer Address:Port Process' 'LISTEN 0 128 127.0.0.1:18081 0.0.0.0:* users:((\"node\",pid=" + pid + ",fd=3))'\n"
	ps := "#!/bin/sh\nprintf x >> \"$OBERTH_METADATA_CALLS\"\nif [ \"$1\" = -A ]; then\n printf '%s\\n' '" + pid + " 1 Fri Oct 2 00:00:00 2026 node metadata-fixture'\n if [ \"$OBERTH_METADATA_MODE\" = stats ]; then exit 0; fi\nelse\n printf '%s\\n' '" + pid + " 0.5 1024 2 S Fri Oct 2 00:00:00 2026'\nfi\nif [ ! -e \"$OBERTH_METADATA_READY\" ]; then\n printf '%s' \"$$\" > \"$OBERTH_METADATA_READY\"\n exec /bin/sleep 0.15\nfi\n"
	for name, text := range map[string]string{"ss": ss, "ps": ps} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("OBERTH_METADATA_READY", ready)
	t.Setenv("OBERTH_METADATA_CALLS", calls)
	t.Setenv("OBERTH_METADATA_CONNECTIONS", filepath.Join(dir, "connections"))
	t.Setenv("OBERTH_METADATA_MODE", "identity")
	return ready, calls
}

func TestDefaultMetadataCancellationDoesNotWarmNextScan(t *testing.T) {
	ready, calls := metadataFixture(t)
	l := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := l.RescanContext(ctx, Include{}); done <- err }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Error("default scanner did not join")
			}
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, _ := os.ReadFile(ready); len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("default scan never reached process enrichment")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("scan cancellation=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("default enrichment did not join")
	}
	if got := l.Status(); got.Seq != 0 || got.LastError != nil {
		t.Fatalf("cancelled scan committed: %+v", got)
	}
	snap, err := l.RescanContext(context.Background(), Include{})
	if err != nil || len(snap.Ports) != 1 {
		t.Fatalf("next observation failed: %v, %+v", err, snap.Ports)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 2 {
		t.Fatalf("cancelled metadata warmed next scan: ps calls=%d, want 2", len(data))
	}
	if !strings.Contains(snap.Ports[0].Command, "metadata-fixture") {
		t.Fatalf("recovery lost process evidence: %+v", snap.Ports[0])
	}
}

func TestDefaultFullStatsCancellationStopsDownstreamCollection(t *testing.T) {
	ready, _ := metadataFixture(t)
	t.Setenv("OBERTH_METADATA_MODE", "stats")
	l := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := l.RescanContext(ctx, Include{Stats: true}); done <- err }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Error("default stats scan did not join")
			}
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, _ := os.ReadFile(ready); len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("default scan never reached full statistics")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stats cancellation=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("default full stats did not join")
	}
	if got := l.Status(); got.Seq != 0 || got.LastError != nil {
		t.Fatalf("cancelled stats scan committed: %+v", got)
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(ready), "connections")); !os.IsNotExist(err) {
		t.Fatalf("cancelled stats admitted connection collection: %q, %v", data, err)
	}
}
