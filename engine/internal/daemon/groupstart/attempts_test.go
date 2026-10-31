package groupstart

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// startResult is what a background drain of a groups.start stream produced. It
// avoids t.Fatal from a goroutine while a test polls state.snapshot.
type startResult struct {
	chunks []rpc.GroupsStartChunk
	end    rpc.GroupsStartEnd
	err    error
}

func drainStartStream(s *client.Stream) <-chan startResult {
	out := make(chan startResult, 1)
	go func() {
		var chunks []rpc.GroupsStartChunk
		for raw := range s.Chunks() {
			var c rpc.GroupsStartChunk
			if err := json.Unmarshal(raw, &c); err != nil {
				out <- startResult{chunks: chunks, err: err}
				return
			}
			chunks = append(chunks, c)
		}
		end := <-s.End()
		if end.Err != nil {
			out <- startResult{chunks: chunks, err: end.Err}
			return
		}
		var summary rpc.GroupsStartEnd
		err := end.Decode(&summary)
		out <- startResult{chunks: chunks, end: summary, err: err}
	}()
	return out
}

// snapshotService returns the group and service row for a config path, or nil
// when the daemon has not published them yet.
func snapshotService(t *testing.T, ctx context.Context, c *client.Client, configPath, service string) (*state.Group, *state.Service) {
	t.Helper()
	var snap state.Snapshot
	if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
		t.Fatalf("state.snapshot: %v", err)
	}
	want := groups.Canonical(configPath)
	for i := range snap.Groups {
		g := &snap.Groups[i]
		if g.ConfigPath == nil || groups.Canonical(*g.ConfigPath) != want {
			continue
		}
		for j := range g.Services {
			if g.Services[j].Name == service {
				return g, &g.Services[j]
			}
		}
	}
	return nil, nil
}

func waitForStarting(t *testing.T, ctx context.Context, c *client.Client, configPath, service, phase string) (*state.Group, *state.Service) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		group, svc := snapshotService(t, ctx, c, configPath, service)
		if svc != nil && svc.Starting != nil && (phase == "" || svc.Starting.Phase == phase) {
			return group, svc
		}
		if time.Now().After(deadline) {
			got := "<no row>"
			if svc != nil {
				got = fmt.Sprintf("%+v", *svc)
			}
			t.Fatalf("never saw service %s starting in phase %q; row = %s", service, phase, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForNoStarting(t *testing.T, ctx context.Context, c *client.Client, configPath, service string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, svc := snapshotService(t, ctx, c, configPath, service)
		if svc == nil || svc.Starting == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("service %s still carries a starting attempt after the start ended: %+v", service, *svc.Starting)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startingChunks(chunks []rpc.GroupsStartChunk) []rpc.GroupsStartChunk {
	var out []rpc.GroupsStartChunk
	for _, c := range chunks {
		if c.State == "starting" {
			out = append(out, c)
		}
	}
	return out
}

func hasPhase(chunks []rpc.GroupsStartChunk, phase string) bool {
	for _, c := range chunks {
		if c.State == "starting" && c.Phase == phase {
			return true
		}
	}
	return false
}

// TestStartAttemptIsVisibleDuringPrepareAndGoneAfterSuccess covers the whole
// reader-visible life of an attempt: a snapshot during prepare shows `starting`
// with the attempt's phase and budget, the service is not running, and after
// the run is registered the attempt is gone and the run row is the truth.
func TestStartAttemptIsVisibleDuringPrepareAndGoneAfterSuccess(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	port := freePort(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: phases
services:
  - name: web
    prepare: sh -c 'sleep 2'
    cmd: %s
    port: %d
`, serviceCmd(t), port))
	c := startDaemon(t, ctx, dir, []int{port})

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path, Progress: true}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()
	result := drainStartStream(s)

	group, svc := waitForStarting(t, ctx, c, path, "web", "prepare")
	if svc.Running {
		t.Fatal("a service in prepare was already reported as running")
	}
	if group.Status == "running" {
		t.Fatalf("group status = %q while only an attempt exists", group.Status)
	}
	if svc.Starting.ReadyTimeout != "30s" {
		t.Errorf("starting.ready_timeout = %q, want the effective 30s", svc.Starting.ReadyTimeout)
	}
	if svc.Starting.StartedAt == "" || svc.Starting.ElapsedMS < 0 {
		t.Errorf("starting = %+v, want a started_at and non-negative elapsed_ms", svc.Starting)
	}
	if len(svc.Starting.WaitingOn) != 0 {
		t.Errorf("starting.waiting_on = %v, want none in prepare", svc.Starting.WaitingOn)
	}

	got := <-result
	if got.err != nil {
		t.Fatalf("stream error: %v", got.err)
	}
	t.Cleanup(func() { killPIDs(got.chunks) })
	if len(got.end.Started) != 1 || got.end.Started[0] != "web" {
		t.Fatalf("end = %+v, want web started", got.end)
	}
	progress := startingChunks(got.chunks)
	if !hasPhase(progress, "prepare") || !hasPhase(progress, "spawning") {
		t.Fatalf("starting chunks = %+v, want prepare and spawning phases", progress)
	}

	waitForNoStarting(t, ctx, c, path, "web")
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, svc = snapshotService(t, ctx, c, path, "web")
		if svc != nil && svc.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("web never became running: %+v", svc)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if svc.Starting != nil {
		t.Fatalf("a running service still carries starting: %+v", svc.Starting)
	}
}

// TestStartAttemptIsClearedOnEveryFailurePath covers dependency failure,
// prepare failure and spawn failure. Each ends the start; none may leave a
// reader-visible attempt behind.
func TestStartAttemptIsClearedOnEveryFailurePath(t *testing.T) {
	skipOnWindows(t)

	t.Run("prepare_failure", func(t *testing.T) {
		dir := isolate(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		path := writeConfig(t, dir, fmt.Sprintf(`name: phases
services:
  - name: web
    prepare: sh -c 'exit 7'
    cmd: %s
    port: %d
`, serviceCmd(t), freePort(t)))
		c := startDaemon(t, ctx, dir, nil)
		chunks, end := runStart(t, ctx, c, path, true)
		if len(end.Errors) != 1 || end.Errors[0] != "web" {
			t.Fatalf("end = %+v, want web failed", end)
		}
		if !hasPhase(startingChunks(chunks), "prepare") {
			t.Fatalf("chunks = %+v, want a prepare progress chunk", chunks)
		}
		waitForNoStarting(t, ctx, c, path, "web")
	})

	t.Run("dependency_failure", func(t *testing.T) {
		old := dependencyTimeout
		dependencyTimeout = 300 * time.Millisecond
		t.Cleanup(func() { dependencyTimeout = old })

		dir := isolate(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		deadPort, livePort := freePort(t), freePort(t)
		path := writeConfig(t, dir, fmt.Sprintf(`name: phases
services:
  - name: ghost
    cmd: sleep 30
    port: %d
  - name: web
    cmd: %s
    port: %d
    depends_on: [ghost]
`, deadPort, serviceCmd(t), livePort))
		c := startDaemon(t, ctx, dir, []int{deadPort, livePort})
		chunks, end := runStart(t, ctx, c, path, true)
		t.Cleanup(func() { killPIDs(chunks) })
		if len(end.Errors) != 1 || end.Errors[0] != "web" {
			t.Fatalf("end = %+v, want only web failed", end)
		}
		progress := startingChunks(chunks)
		foundWaiting := false
		for _, c := range progress {
			if c.Service == "web" && c.Phase == phaseWaitingDependency && len(c.WaitingOn) == 1 && c.WaitingOn[0] == "ghost" {
				foundWaiting = true
			}
		}
		if !foundWaiting {
			t.Fatalf("starting chunks = %+v, want web waiting on ghost", progress)
		}
		waitForNoStarting(t, ctx, c, path, "web")
	})

	t.Run("spawn_failure", func(t *testing.T) {
		dir := isolate(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		path := writeConfig(t, dir, `name: phases
services:
  - name: web
    cmd: /nonexistent/oberth-test-binary
`)
		c := startDaemon(t, ctx, dir, nil)
		_, end := runStart(t, ctx, c, path, true)
		if len(end.Errors) != 1 || end.Errors[0] != "web" {
			t.Fatalf("end = %+v, want web failed", end)
		}
		waitForNoStarting(t, ctx, c, path, "web")
	})
}

// TestStartAttemptIsClearedOnCancellation: stream.cancel stops the start call,
// and the attempt must disappear with it.
func TestStartAttemptIsClearedOnCancellation(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)
	daemonCtx, stopDaemon := context.WithCancel(context.Background())
	defer stopDaemon()
	path := writeConfig(t, dir, fmt.Sprintf(`name: phases
services:
  - name: web
    prepare: sh -c 'sleep 5'
    cmd: %s
    port: %d
`, serviceCmd(t), freePort(t)))
	c := startDaemon(t, daemonCtx, dir, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path, Progress: true}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	result := drainStartStream(s)
	waitForStarting(t, daemonCtx, c, path, "web", "prepare")
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatalf("stream.cancel: %v", err)
	}
	got := <-result
	if got.err != nil {
		t.Fatalf("cancelled stream reported %v", got.err)
	}
	waitForNoStarting(t, daemonCtx, c, path, "web")
}

// TestStartProgressIsGatedByProgress: without progress the stream carries no
// `starting` chunk; with it, the phase switches are pushed as they happen.
func TestStartProgressIsGatedByProgress(t *testing.T) {
	skipOnWindows(t)

	for _, tc := range []struct {
		name     string
		progress bool
		want     bool
	}{
		{name: "off", progress: false, want: false},
		{name: "on", progress: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolate(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			port := freePort(t)
			path := writeConfig(t, dir, fmt.Sprintf(`name: phases
services:
  - name: web
    prepare: sh -c 'sleep 0.4'
    cmd: %s
    port: %d
`, serviceCmd(t), port))
			c := startDaemon(t, ctx, dir, []int{port})
			chunks, end := runStartWithProgress(t, ctx, c, path, tc.progress)
			t.Cleanup(func() { killPIDs(chunks) })
			if len(end.Started) != 1 {
				t.Fatalf("end = %+v, want web started", end)
			}
			got := len(startingChunks(chunks)) > 0
			if got != tc.want {
				t.Fatalf("progress chunks present = %v, want %v; chunks = %+v", got, tc.want, chunks)
			}
		})
	}
}

// TestStartAttemptAppearsInScopedSnapshot: the attempt is attached before the
// scope projection, so a scoped reader selecting the worktree still sees it.
func TestStartAttemptAppearsInScopedSnapshot(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: phases
services:
  - name: web
    prepare: sh -c 'sleep 2'
    cmd: %s
    port: %d
`, serviceCmd(t), port))
	c := startDaemon(t, ctx, dir, []int{port})

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()
	result := drainStartStream(s)
	waitForStarting(t, ctx, c, path, "web", "prepare")

	scope := state.Scope{Worktrees: []string{groups.Canonical(dir)}}
	var scoped rpc.StateSnapshotResult
	if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{Scope: &scope}, &scoped); err != nil {
		t.Fatalf("scoped state.snapshot: %v", err)
	}
	found := false
	for _, wt := range scoped.Worktrees {
		for _, svc := range wt.Services {
			if svc.Name != "web" {
				continue
			}
			if svc.Starting == nil || svc.Starting.Phase != "prepare" {
				t.Fatalf("scoped web = %+v, want a prepare attempt", svc)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("scoped snapshot did not include web: %+v", scoped.Worktrees)
	}

	got := <-result
	t.Cleanup(func() { killPIDs(got.chunks) })
}

// TestAnAlreadyRunningServiceOwnsNoAttempt: the skip path never creates one.
func TestAnAlreadyRunningServiceOwnsNoAttempt(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := writeConfig(t, dir, `name: phases
services:
  - name: worker
    cmd: sleep 30
`)
	c := startDaemon(t, ctx, dir, nil)

	first, firstEnd := runStart(t, ctx, c, path, true)
	t.Cleanup(func() { killPIDs(first) })
	if len(firstEnd.Started) != 1 {
		t.Fatalf("first end = %+v", firstEnd)
	}
	second, secondEnd := runStart(t, ctx, c, path, true)
	if len(secondEnd.Skipped) != 1 || len(secondEnd.Started) != 0 {
		t.Fatalf("second end = %+v, want worker skipped", secondEnd)
	}
	if len(startingChunks(second)) != 0 {
		t.Fatalf("skipped start emitted progress: %+v", second)
	}
	waitForNoStarting(t, ctx, c, path, "worker")
}

func runStart(t *testing.T, ctx context.Context, c *client.Client, path string, progress bool) ([]rpc.GroupsStartChunk, rpc.GroupsStartEnd) {
	t.Helper()
	return runStartWithProgress(t, ctx, c, path, progress)
}

func runStartWithProgress(t *testing.T, ctx context.Context, c *client.Client, path string, progress bool) ([]rpc.GroupsStartChunk, rpc.GroupsStartEnd) {
	t.Helper()
	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path, Progress: progress}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()
	return collect(t, s)
}

// TestPrepareDurationSidecarIsIndependentAndAdvisory covers the advisory
// duration record: its own key, its own file, and no effect on the input
// fingerprint.
func TestPrepareDurationSidecarIsIndependentAndAdvisory(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	const config = "/repo/oberth.yaml"

	if got := lastPrepareDuration(config, "web"); got != nil {
		t.Fatalf("a fresh duration = %d, want nil", *got)
	}
	if err := recordPrepareDuration(config, "web", 1250*time.Millisecond); err != nil {
		t.Fatalf("recordPrepareDuration: %v", err)
	}
	got := lastPrepareDuration(config, "web")
	if got == nil || *got != 1250 {
		t.Fatalf("duration = %v, want 1250", got)
	}
	if other := lastPrepareDuration(config, "api"); other != nil {
		t.Fatalf("another service inherited the duration: %d", *other)
	}
	if other := lastPrepareDuration("/repo/other/oberth.yaml", "web"); other != nil {
		t.Fatalf("another checkout inherited the duration: %d", *other)
	}
	if prepareDurationPath(config, "web") == prepareStampPath(config, "web") {
		t.Fatal("the duration and the input fingerprint share a file")
	}
	if err := os.WriteFile(prepareDurationPath(config, "web"), []byte("nonsense\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lastPrepareDuration(config, "web"); got != nil {
		t.Fatalf("a malformed sidecar = %d, want nil", *got)
	}
}
