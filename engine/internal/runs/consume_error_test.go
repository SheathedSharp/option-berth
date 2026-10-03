package runs

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func TestConsumeLegacyPreservesInvalidEvidence(t *testing.T) {
	for _, content := range []string{
		`{"runs":`,
		`null`,
		`{}`,
		`{"runs":null}`,
		fmt.Sprintf(`{"runs":{"%d":{"pid":%d,"id":"mismatched"}}}`, os.Getpid(), os.Getpid()+1),
	} {
		t.Run(content, func(t *testing.T) {
			t.Setenv("BERTH_HOME", t.TempDir())
			if err := os.WriteFile(Path(), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			called := false
			err := ConsumeLegacy(func([]Entry) ([]Entry, error) {
				called = true
				return nil, nil
			})
			if err == nil || called {
				t.Fatalf("invalid recovery reached destructive transform: called=%v error=%v", called, err)
			}
			after, readErr := os.ReadFile(Path())
			if readErr != nil || !bytes.Equal(after, []byte(content)) {
				t.Fatalf("failed recovery changed disk evidence: %q, %v", after, readErr)
			}
		})
	}
}

func TestConsumeLegacyDistinguishesMissingAndUnreadable(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	calls := 0
	if err := ConsumeLegacy(func(entries []Entry) ([]Entry, error) {
		calls++
		if len(entries) != 0 {
			t.Fatal("new registry is not empty")
		}
		return nil, nil
	}); err != nil || calls != 1 {
		t.Fatalf("missing registry: calls=%d error=%v", calls, err)
	}
	if err := os.Mkdir(Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ConsumeLegacy(func([]Entry) ([]Entry, error) {
		t.Fatal("unreadable registry reached transform")
		return nil, nil
	}); err == nil {
		t.Fatal("unreadable registry was treated as missing")
	}
	if st, err := os.Stat(Path()); err != nil || !st.IsDir() {
		t.Fatal("unreadable evidence removed")
	}
}
