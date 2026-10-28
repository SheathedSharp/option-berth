package groupstart

import (
	"errors"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
)

// A refusal already carries the way out. Replacing that with a "read the logs"
// guess sent an agent to a file that was never written, because no process ever
// existed to write it.
func TestDetailAndHintKeepsARefusalsOwnWayOut(t *testing.T) {
	refused := rpc.NewError(rpc.CodeOutsideHome,
		"/tmp/project is outside /Users/someone",
		"pass allow_outside_home: true to start commands outside your home directory")

	message, hint := detailAndHint(refused)
	if message != "/tmp/project is outside /Users/someone" {
		t.Fatalf("message = %q, want the refusal's own detail", message)
	}
	if hint != "pass allow_outside_home: true to start commands outside your home directory" {
		t.Fatalf("hint = %q, want the refusal's own hint", hint)
	}
}

// A failure that never reached a process carries no hint of its own, and the
// caller must not invent one: an empty field is honest, a pointer at a log that
// does not exist is not.
func TestDetailAndHintLeavesAPlainRefusalWithoutAHint(t *testing.T) {
	message, hint := detailAndHint(errors.New("service api has no cmd to run"))
	if message != "service api has no cmd to run" || hint != "" {
		t.Fatalf("message/hint = %q/%q, want the message and no hint", message, hint)
	}
}

// Waiting on a dependency fails before this service ever runs, so the log worth
// reading belongs to the dependency that never became ready — not to the
// service that waited, and not to the first dependency the manifest lists.
func TestDependencyHintNamesTheDependencyThatFailed(t *testing.T) {
	err := dependencyFailure{
		message: "timed out after 3s waiting for cache on port 6379",
		names:   []string{"cache"},
	}
	if got := dependencyHint(err); got != "oberth logs cache --once" {
		t.Fatalf("dependencyHint = %q, want the dependency that never became ready", got)
	}
	if got := dependencyHint(errors.New("spawn failed")); got != "" {
		t.Fatalf("dependencyHint = %q, want no hint for an unrelated failure", got)
	}
}

// The typed failure must keep the message the classifier reads, or a dependency
// timeout would stop being reported as one.
func TestDependencyFailureKeepsItsMessage(t *testing.T) {
	timeout := dependencyFailure{message: "timed out after 3s waiting for cache", names: []string{"cache"}}
	if startFailureReason(timeout.Error()) != "dependency_timeout" {
		t.Fatalf("reason = %q, want dependency_timeout", startFailureReason(timeout.Error()))
	}
	noPort := dependencyFailure{message: "no port for dependency worker: not claimed", names: []string{"worker"}}
	if startFailureReason(noPort.Error()) != "dependency_not_ready" {
		t.Fatalf("reason = %q, want dependency_not_ready", startFailureReason(noPort.Error()))
	}
	if !strings.Contains(timeout.Error(), "cache") {
		t.Fatalf("message = %q, want it to name the dependency", timeout.Error())
	}
}
