package runsreg

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/store"
)

// These tests exercise startup failure ordering only. They do not replace
// real SQLite, legacy file locking, or whole-daemon cleanup regression tests.
type requiredRecoveryFixture struct {
	calls     []string
	seenStore *store.Store
	exitsErr  error
	importErr error
	imported  int
}

func (f *requiredRecoveryFixture) LoadExits(st *store.Store) error {
	f.calls = append(f.calls, "exits")
	f.seenStore = st
	return f.exitsErr
}

func (f *requiredRecoveryFixture) ImportLegacy() (int, error) {
	f.calls = append(f.calls, "import")
	return f.imported, f.importErr
}

var _ registryStartupRecovery = (*Registry)(nil)

func TestRequiredRecoveryStopsBeforeImportOnExitReadFailure(t *testing.T) {
	cause := errors.New("exit history unavailable")
	f := &requiredRecoveryFixture{exitsErr: cause, imported: 4}
	n, err := restoreRequiredRegistry(f, &store.Store{})
	if n != 0 || !errors.Is(err, cause) || !strings.Contains(err.Error(), "restore run exit history") {
		t.Fatalf("count=%d error=%v", n, err)
	}
	if !reflect.DeepEqual(f.calls, []string{"exits"}) {
		t.Fatalf("legacy import ran after failed recovery: %v", f.calls)
	}
}

func TestRequiredRecoveryPreservesImportFailure(t *testing.T) {
	cause := errors.New("live run ledger unavailable")
	f := &requiredRecoveryFixture{importErr: cause, imported: 4}
	n, err := restoreRequiredRegistry(f, nil)
	if n != 0 || !errors.Is(err, cause) || !strings.Contains(err.Error(), "restore live run registry") {
		t.Fatalf("count=%d error=%v", n, err)
	}
	if !reflect.DeepEqual(f.calls, []string{"exits", "import"}) {
		t.Fatalf("wrong recovery order: %v", f.calls)
	}
}

func TestRequiredRecoveryPreservesStorelessAndDurableModes(t *testing.T) {
	for _, durable := range []bool{false, true} {
		var st *store.Store
		if durable {
			// Only an identity token for this fixture; no database method is called.
			st = &store.Store{}
		}
		f := &requiredRecoveryFixture{imported: 3}
		n, err := restoreRequiredRegistry(f, st)
		if err != nil || n != 3 || f.seenStore != st {
			t.Fatalf("durable=%v count=%d error=%v", durable, n, err)
		}
		if !reflect.DeepEqual(f.calls, []string{"exits", "import"}) {
			t.Fatalf("durable=%v calls=%v", durable, f.calls)
		}
	}
}

func TestRequiredRecoveryRetriesBothStagesAfterReadFailure(t *testing.T) {
	f := &requiredRecoveryFixture{exitsErr: errors.New("temporary read failure"), imported: 2}
	if n, err := restoreRequiredRegistry(f, nil); err == nil || n != 0 {
		t.Fatalf("failed first attempt: count=%d error=%v", n, err)
	}
	f.exitsErr = nil
	n, err := restoreRequiredRegistry(f, nil)
	if err != nil || n != 2 || !reflect.DeepEqual(f.calls, []string{"exits", "exits", "import"}) {
		t.Fatalf("retry: count=%d error=%v calls=%v", n, err, f.calls)
	}
}
