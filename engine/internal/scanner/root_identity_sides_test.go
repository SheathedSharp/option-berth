package scanner

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

func TestAnEmptyKnownRootListDoesNotRereadManifests(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "name: before\n")
	l := New(Options{})
	l.ensureIndex()
	l.attr.index.Observe(dir)
	writeConfig(t, dir, "name: after\n")
	l.seedRoots(&countingRoots{})
	names := configNames(l)
	if len(names) != 1 || names[0] != "before" {
		t.Fatalf("names = %v, an empty known-root list reread the manifest", names)
	}
}

func configNames(l *Loop) []string {
	var names []string
	for _, cfg := range l.attr.index.Configs() {
		if cfg != nil {
			names = append(names, cfg.Name)
		}
	}
	return names
}

func TestANewlySeenRootIsRecordedOnceAndNotInsertedAgain(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "name: demo\n")
	l := New(Options{})
	l.ensureIndex()
	other := t.TempDir()
	writeConfig(t, other, "name: other\n")
	l.attr.index.Observe(other)
	st := &countingRoots{roots: []string{dir}}
	l.seedRoots(st)
	// The ledger stores the canonical checkout root, not the spelling the
	// store happened to keep.
	if len(st.got) != 1 || st.got[0].Root != groups.Canonical(dir) {
		t.Fatalf("identities = %+v, the newly seen root was not recorded on its own", st.got)
	}

	alone := t.TempDir()
	writeConfig(t, alone, "name: alone\n")
	one := New(Options{})
	one.ensureIndex()
	roots := &countingRoots{roots: []string{alone}}
	one.seedRoots(roots)
	one.rememberRoots(roots, one.attr.index)
	if roots.adds != 0 {
		t.Fatalf("adds = %d, a root the seed already accepted was inserted again", roots.adds)
	}
}

func TestAFailedVanishedIdentityIsRetriedAndAStoredOneIsNot(t *testing.T) {
	dir := t.TempDir()
	warns := &warnCount{}
	ledger := &identityLedger{fail: 1}
	l := New(Options{Logger: slog.New(warns)})
	l.recordIdentityRoot(ledger, dir)
	if warns.n != 1 || len(ledger.got) != 0 || len(l.attr.identities) != 0 {
		t.Fatalf("warns=%d records=%d cached=%d, a failed vanished identity was stored", warns.n, len(ledger.got), len(l.attr.identities))
	}
	l.recordIdentityRoot(ledger, dir)
	l.recordIdentityRoot(ledger, dir)
	if len(ledger.got) != 1 {
		t.Fatalf("records = %+v, a stored vanished identity was written twice", ledger.got)
	}
}

type aliasLedger struct {
	identityLedger
	aliases map[string]string
	err     error
}

func (s *aliasLedger) GroupAliases() (map[string]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.aliases, nil
}

func TestAFailedAliasReadKeepsThePreviousName(t *testing.T) {
	dir := t.TempDir()
	warns := &warnCount{}
	l := New(Options{Logger: slog.New(warns)})
	l.ensureIndex()
	st := &aliasLedger{aliases: map[string]string{dir: "pretty"}}
	l.loadAliases(st)
	if got := l.attr.index.ProjectName(dir); got != "pretty" || warns.n != 0 {
		t.Fatalf("name=%q warns=%d, a successful alias read was not applied", got, warns.n)
	}
	st.err = errors.New("ledger unavailable")
	l.loadAliases(st)
	if got := l.attr.index.ProjectName(dir); got != "pretty" || warns.n != 1 {
		t.Fatalf("name=%q warns=%d, a failed alias read replaced the previous name", got, warns.n)
	}
}
