package runsreg

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestNoGroupRunsIncludesStoppingAndRenamedManifest(t *testing.T) {
	config := filepath.Join(t.TempDir(), "oberth.yaml")
	cases := []struct {
		name    string
		rec     Record
		blocked bool
	}{
		{"live", Record{Group: "demo"}, true},
		{"compatibility stopping", Record{Group: "demo", stopping: true}, true},
		{"case folded group", Record{Group: "DEMO"}, true},
		{"renamed manifest", Record{Group: "renamed", ConfigPath: config}, true},
		{"unrelated", Record{Group: "other"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			r.Mirror = false
			tc.rec.PID = 424242
			tc.rec.StartedAt = time.Now()
			r.Register(tc.rec)
			called := false
			n, err := r.WithNoGroupRuns(context.Background(), "demo", config, func() (int, error) { called = true; return 1, nil })
			if tc.blocked {
				if err == nil || n != 0 || called {
					t.Fatalf("released with recorded run: %d %v %v", n, err, called)
				}
			} else if err != nil || n != 1 || !called {
				t.Fatalf("unrelated group blocked: %d %v", n, err)
			}
		})
	}
}

func TestNoGroupRunsHoldsMutationLockWithoutBlockingReads(t *testing.T) {
	r := New()
	r.Mirror = false
	registered := make(chan struct{})
	n, err := r.WithNoGroupRuns(context.Background(), "demo", "", func() (int, error) {
		if r.mirrorMu.TryLock() {
			r.mirrorMu.Unlock()
			t.Error("release has no registration exclusion")
		}
		// This read would deadlock if mu, rather than mirrorMu, spanned store I/O.
		if _, ok := r.Lookup(424242); ok {
			t.Error("unexpected record")
		}
		go func() { r.Register(Record{PID: 424242, Group: "demo", StartedAt: time.Now()}); close(registered) }()
		return 3, nil
	})
	if err != nil || n != 3 {
		t.Fatalf("release = %d, %v", n, err)
	}
	<-registered
	if _, ok := r.Lookup(424242); !ok {
		t.Fatal("subsequent registration was lost")
	}
	called := false
	if _, err := r.WithNoGroupRuns(context.Background(), "demo", "", func() (int, error) { called = true; return 0, nil }); err == nil || called {
		t.Fatal("new generation did not prevent another release")
	}
}

func TestNoGroupRunsRefusesBusyOrCanceledMutation(t *testing.T) {
	r := New()
	r.Mirror = false
	mutate := func() (int, error) { t.Error("mutation must not run"); return 0, nil }
	r.mirrorMu.Lock()
	_, err := r.WithNoGroupRuns(context.Background(), "demo", "", mutate)
	r.mirrorMu.Unlock()
	if err == nil {
		t.Fatal("busy registry was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.WithNoGroupRuns(ctx, "demo", "", mutate); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}

func TestNoGroupRunsPropagatesStoreFailureAndUnlocks(t *testing.T) {
	r := New()
	r.Mirror = false
	refused := errors.New("store refused")
	if n, err := r.WithNoGroupRuns(context.Background(), "demo", "", func() (int, error) { return 0, refused }); n != 0 || !errors.Is(err, refused) {
		t.Fatalf("release = %d, %v", n, err)
	}
	if !r.mirrorMu.TryLock() {
		t.Fatal("failed release retained mutation lock")
	}
	r.mirrorMu.Unlock()
}
