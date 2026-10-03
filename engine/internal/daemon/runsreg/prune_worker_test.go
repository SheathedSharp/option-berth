package runsreg

import (
	"sync"
	"testing"
	"time"
)

func TestPruningShutdownJoinsInflightMutation(t *testing.T) {
	stopPruning()
	stop, done := make(chan struct{}), make(chan struct{})
	entered, unblock := make(chan struct{}), make(chan struct{})
	ticks := make(chan time.Time, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	t.Cleanup(func() { release(); stopPruning() })
	pruning.Lock()
	pruning.stop, pruning.done = stop, done
	pruning.Unlock()
	go func() { defer close(done); runPruning(stop, ticks, func() { close(entered); <-unblock }) }()
	ticks <- time.Now()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("prune did not start")
	}
	stopped := make(chan struct{})
	go func() { stopPruning(); close(stopped) }()
	select {
	case <-stop:
	case <-time.After(time.Second):
		t.Fatal("stop not requested")
	}
	select {
	case <-stopped:
		t.Fatal("shutdown returned before mutation finished")
	default:
	}
	release()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join")
	}
	select {
	case <-done:
	default:
		t.Fatal("worker still running")
	}
}

func TestPruningCancelledTickDoesNotStartMutation(t *testing.T) {
	stop := make(chan struct{})
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	close(stop)
	calls := 0
	runPruning(stop, ticks, func() { calls++ })
	if calls != 0 {
		t.Fatal("stopped worker began another mutation")
	}
}
