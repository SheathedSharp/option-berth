package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotUsesCurrentManifestBytesWithoutListeners(t *testing.T) {
	st := openStore(t)
	dir := resolved(t, t.TempDir())
	writeConfig(t, dir, "name: before\nservices:\n  - name: api\n    cmd: old\n")
	path := filepath.Join(dir, ".oberth.yaml")
	if err := st.AddRoot(dir); err != nil {
		t.Fatal(err)
	}
	l := loopWith(st)
	first, err := l.Rescan(Include{})
	if err != nil || len(first.Groups) != 1 {
		t.Fatalf("fixture snapshot: %+v %v", first, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, "name: afterx\nservices:\n  - name: api\n    cmd: new\n")
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	next, err := l.Rescan(Include{})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Groups) != 1 || next.Groups[0].Name != "afterx" || next.Groups[0].Services[0].Cmd != "new" {
		t.Fatal("listener-free snapshot retained superseded manifest")
	}
	if first.Groups[0].Name != "before" || first.Groups[0].Services[0].Cmd != "old" {
		t.Fatal("old snapshot mutated after config refresh")
	}
	if next.Seq <= first.Seq {
		t.Fatal("config change did not advance publication")
	}
}

func TestSnapshotDiscoversFirstManifestInStoredSilentRoot(t *testing.T) {
	st := openStore(t)
	dir := resolved(t, t.TempDir())
	if err := st.AddRoot(dir); err != nil {
		t.Fatal(err)
	}
	l := loopWith(st)
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, "name: appeared\nservices:\n  - name: worker\n    cmd: run\n")
	next, err := l.Rescan(Include{})
	if err != nil || len(next.Groups) != 1 || next.Groups[0].Name != "appeared" {
		t.Fatalf("silent stored root missed new manifest: %+v %v", next.Groups, err)
	}
}
