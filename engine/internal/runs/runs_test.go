package runs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func isolateRunsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func mustAdd(t *testing.T, entry Entry) {
	t.Helper()
	if err := Add(entry); err != nil {
		t.Fatalf("Add(%+v): %v", entry, err)
	}
}

func mustRemove(t *testing.T, pid int) {
	t.Helper()
	if err := Remove(pid); err != nil {
		t.Fatalf("Remove(%d): %v", pid, err)
	}
}

func writeRunsFile(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".option-berth")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPathSitsBesideTheConfig(t *testing.T) {
	t.Setenv("HOME", "/tmp/runs-fixture-home")
	want := filepath.Join("/tmp/runs-fixture-home", ".option-berth", "runs.json")
	if got := Path(); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestLoadWithoutAFileIsEmpty(t *testing.T) {
	isolateRunsHome(t)
	reg := Load()
	if reg == nil || reg.Runs == nil {
		t.Fatal("a missing file produced a nil registry")
	}
	if len(reg.Runs) != 0 {
		t.Errorf("a missing file produced %+v", reg.Runs)
	}
}

func TestAddThenLoadRoundTrips(t *testing.T) {
	isolateRunsHome(t)
	self := os.Getpid()

	if err := Add(Entry{PID: self, Tag: "demo", ID: "abc123", Cmd: "python -m http.server"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	entry, found := Load().LookupByPID(self)
	if !found {
		t.Fatalf("pid %d is missing after Add", self)
	}
	if entry.Tag != "demo" || entry.ID != "abc123" {
		t.Errorf("round trip = %+v", entry)
	}
	if entry.StartedAt == "" {
		t.Error("Add did not stamp StartedAt")
	}
}

func TestRemoveDeletesAndIsIdempotent(t *testing.T) {
	isolateRunsHome(t)
	self := os.Getpid()
	mustAdd(t, Entry{PID: self, Tag: "x"})
	mustRemove(t, self)
	if _, found := Load().LookupByPID(self); found {
		t.Error("the entry survived Remove")
	}
	if err := Remove(999999); err != nil {
		t.Errorf("Remove of an unrecorded pid = %v, want nil", err)
	}
}

func TestLoadPrunesDeadPIDsAndPersists(t *testing.T) {
	isolateRunsHome(t)
	self := os.Getpid()
	dead := spawnReaped(t)

	mustAdd(t, Entry{PID: self, Tag: "live"})
	mustAdd(t, Entry{PID: dead, Tag: "dead"})

	reg := Load()
	if _, found := reg.LookupByPID(self); !found {
		t.Errorf("the live pid %d was pruned", self)
	}
	if _, found := reg.LookupByPID(dead); found {
		t.Errorf("the dead pid %d survived", dead)
	}
	if _, found := load().Runs[dead]; found {
		t.Error("the dead pid was not removed from disk")
	}
}

func TestLoadRecoversFromAMalformedFile(t *testing.T) {
	home := isolateRunsHome(t)
	writeRunsFile(t, home, "{not json")
	reg := Load()
	if reg == nil || reg.Runs == nil || len(reg.Runs) != 0 {
		t.Errorf("a malformed file recovered to %+v, want empty", reg)
	}
}

// spawnReaped starts a trivial process, waits for it to exit and returns its
// now-dead pid.
func spawnReaped(t *testing.T) int {
	t.Helper()
	command := exec.Command("true")
	if err := command.Start(); err != nil {
		command = exec.Command("sh", "-c", "exit 0")
		if err := command.Start(); err != nil {
			t.Skipf("cannot spawn a helper process: %v", err)
		}
	}
	pid := command.Process.Pid
	_ = command.Wait()
	return pid
}

// The runs mirror may be the first writer of ~/.option-berth, so it must not
// leave a directory other users can list.
func TestAddCreatesTheDirectoryOwnerOnly(t *testing.T) {
	isolateRunsHome(t)
	if err := Add(Entry{PID: os.Getpid(), Tag: "t"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	info, err := os.Stat(filepath.Dir(Path()))
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("config dir mode = %o, want 700", perm)
	}
}
