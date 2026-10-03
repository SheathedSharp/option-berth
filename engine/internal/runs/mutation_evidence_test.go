package runs

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func TestMutationsPreserveInvalidRegistryEvidence(t *testing.T) {
	operations := map[string]func() error{
		"add":         func() error { return Add(Entry{PID: os.Getpid(), ID: "new"}) },
		"remove":      func() error { return Remove(os.Getpid()) },
		"conditional": func() error { _, err := RemoveIfMatch(Entry{PID: os.Getpid(), ID: "observed"}); return err },
	}
	for name, mutate := range operations {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BERTH_HOME", t.TempDir())
			for _, invalid := range []string{
				`{"runs":`, `null`, `{}`, `{"runs":null}`,
				fmt.Sprintf(`{"runs":{"%d":{"pid":%d,"id":"observed"}}}`, os.Getpid(), os.Getpid()+1),
			} {
				if err := os.WriteFile(Path(), []byte(invalid), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := mutate(); err == nil {
					t.Fatalf("mutation accepted invalid evidence: %s", invalid)
				}
				data, err := os.ReadFile(Path())
				if err != nil || !bytes.Equal(data, []byte(invalid)) {
					t.Fatal("mutation overwrote invalid evidence")
				}
			}
		})
	}
}

func TestMutationsDistinguishMissingAndUnreadableRegistry(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	if err := Add(Entry{PID: os.Getpid(), ID: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Add(Entry{PID: os.Getpid(), ID: "new"}); err == nil {
		t.Fatal("directory treated as missing")
	}
	if st, err := os.Stat(Path()); err != nil || !st.IsDir() {
		t.Fatal("unreadable evidence replaced")
	}
}
