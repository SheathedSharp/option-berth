package cmd

import (
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
)

// The protocol schema and the dispatcher have to name the same methods. This
// package links every package that registers a handler, so the registry here
// is the one the shipped binary serves: a method the schema describes but
// nothing serves is a promise the daemon cannot keep, and a served method the
// schema omits is wire nobody can discover.
func TestDescribedMethodsAreExactlyTheServedOnes(t *testing.T) {
	served := map[string]bool{}
	for _, m := range daemon.RegisteredMethods() {
		served[m] = true
	}
	described := rpc.Methods()
	for m := range described {
		if !served[m] {
			t.Errorf("method %q is described in the schema but no handler serves it", m)
		}
	}
	for m := range served {
		if _, ok := described[m]; !ok {
			t.Errorf("method %q is served but not described in the schema", m)
		}
	}
}
