package ports

import (
	"errors"
	"testing"
)

// capturedLsof mirrors `lsof -a -p … -d cwd -Fpn` output from a run where one
// requested pid had exited: lsof printed what it resolved and exited non-zero.
const capturedLsof = `p88001
n/srv/shop
p88002
n/srv/shop/web
p314
n/
`

// nonZeroExit stands in for the *exec.ExitError Output returns on a non-zero
// exit; cwdsFromLsof only checks whether it is nil.
var nonZeroExit = errors.New("exit status 1")

func TestCwdsFromLsofKeepsResolvedRecordsOnFailure(t *testing.T) {
	cwds := cwdsFromLsof([]byte(capturedLsof), nonZeroExit)

	want := map[int]string{
		88001: "/srv/shop",
		88002: "/srv/shop/web",
		314:   "/",
	}
	if len(cwds) != len(want) {
		t.Fatalf("got %d cwds, want %d: %v", len(cwds), len(want), cwds)
	}
	for pid, cwd := range want {
		if cwds[pid] != cwd {
			t.Errorf("cwd of %d = %q, want %q", pid, cwds[pid], cwd)
		}
	}
}

func TestCwdsFromLsofParsesACleanRun(t *testing.T) {
	cwds := cwdsFromLsof([]byte("p5151\nn/var/data\n"), nil)
	if cwds[5151] != "/var/data" {
		t.Errorf("cwd of 5151 = %q, want /var/data", cwds[5151])
	}
}

func TestCwdsFromLsofGivesUpOnlyOnNoOutput(t *testing.T) {
	if cwds := cwdsFromLsof(nil, nonZeroExit); len(cwds) != 0 {
		t.Errorf("got %v, want no cwds without output", cwds)
	}
	if cwds := cwdsFromLsof(nil, errors.New(`exec: "lsof": executable file not found`)); len(cwds) != 0 {
		t.Errorf("got %v, want no cwds when lsof is missing", cwds)
	}
}

func TestCwdsFromLsofIgnoresPathsWithoutAProcess(t *testing.T) {
	// A path before any process, and a non-numeric process record, must not
	// attribute a cwd to pid 0 or to the previous process.
	cwds := cwdsFromLsof([]byte("n/orphan\np5151\nn/good\npNaN\nn/lost\n"), nil)
	if len(cwds) != 1 || cwds[5151] != "/good" {
		t.Errorf("got %v, want only 5151 -> /good", cwds)
	}
}
