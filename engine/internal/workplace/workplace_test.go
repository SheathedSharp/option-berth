package workplace

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

func list() []Workplace {
	return []Workplace{
		{ID: "aaaa1111", Name: "default", Worktree: "/w", Active: true},
		{ID: "bbbb2222", Name: "spike", Worktree: "/w"},
		{ID: "cccc3333", Name: "spike-2", Worktree: "/w"},
	}
}

func TestResolvePrefersTheExactName(t *testing.T) {
	got, err := Resolve(list(), "spike")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != "bbbb2222" {
		t.Fatalf("Resolve(\"spike\") = %q, want the exact name", got.ID)
	}
	// Case does not decide which workplace a name means.
	if got, err = Resolve(list(), "SPIKE"); err != nil || got.ID != "bbbb2222" {
		t.Fatalf("Resolve(\"SPIKE\") = (%q, %v)", got.ID, err)
	}
}

func TestResolveRefusesAmbiguity(t *testing.T) {
	// "spike" is a name and also the prefix of "spike-2"; the name tier wins,
	// so this is not ambiguous.
	if got, err := Resolve(list(), "spike"); err != nil || got.Name != "spike" {
		t.Fatalf("Resolve(\"spike\") = (%+v, %v)", got, err)
	}
	// A prefix that only matches by name in two places is ambiguous, and is
	// never settled by picking one.
	all := list()
	all[1].Name = "release-notes"
	all[2].Name = "release-branch"
	_, err := Resolve(all, "release")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Resolve(ambiguous) = %v, want ErrAmbiguous", err)
	}
	if Code(err) != rpc.CodeAmbiguous {
		t.Fatalf("Code(ambiguous) = %d, want %d", Code(err), rpc.CodeAmbiguous)
	}
	if Hint(err) == "" {
		t.Fatal("an ambiguous selector has no hint")
	}
}

func TestResolveByIdAndPrefix(t *testing.T) {
	got, err := Resolve(list(), "cccc3333")
	if err != nil || got.Name != "spike-2" {
		t.Fatalf("Resolve by id = (%+v, %v)", got, err)
	}
	got, err = Resolve(list(), "cccc")
	if err != nil || got.Name != "spike-2" {
		t.Fatalf("Resolve by id prefix = (%+v, %v)", got, err)
	}
	if _, err := Resolve(list(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(unknown) = %v, want ErrNotFound", err)
	}
	if _, err := Resolve(list(), "   "); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(empty) = %v, want ErrNotFound", err)
	}
	if _, err := Resolve(nil, "anything"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve with no workplaces = %v, want ErrNotFound", err)
	}
}

func TestActivePicksTheOneMarkedActive(t *testing.T) {
	got, ok := Active(list())
	if !ok || got.Name != "default" {
		t.Fatalf("Active = (%+v, %v)", got, ok)
	}
	if _, ok := Active(nil); ok {
		t.Fatal("Active reported one where there is none")
	}
}

func TestValidateNameRefusesWhatCannotBeRendered(t *testing.T) {
	if got, err := ValidateName("  spike  "); err != nil || got != "spike" {
		t.Fatalf("ValidateName trimmed = (%q, %v)", got, err)
	}
	if _, err := ValidateName("   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name = %v, want ErrInvalid", err)
	}
	if _, err := ValidateName(strings.Repeat("x", MaxNameRunes+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long name = %v, want ErrInvalid", err)
	}
	if _, err := ValidateName("two\nlines"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("control character = %v, want ErrInvalid", err)
	}
	// A task title is a legitimate name, spaces and all.
	if got, err := ValidateName("fix the flaky test"); err != nil || got != "fix the flaky test" {
		t.Fatalf("ValidateName with spaces = (%q, %v)", got, err)
	}
}

func TestRecordValidateAndDefaults(t *testing.T) {
	base := Record{Kind: KindShell}
	if err := base.Validate(); err != nil {
		t.Fatalf("a minimal shell record was refused: %v", err)
	}
	withDefaults := base.WithDefaults()
	if withDefaults.Source != SourceReported {
		t.Fatalf("an explicit report defaulted to source %q, want %q", withDefaults.Source, SourceReported)
	}
	if withDefaults.Status != StatusRunning {
		t.Fatalf("a record with no outcome defaulted to %q, want running", withDefaults.Status)
	}
	if withDefaults.ID == "" || withDefaults.StartedAt.IsZero() {
		t.Fatalf("defaults left id/started_at empty: %+v", withDefaults)
	}
	if !withDefaults.EndedAt.IsZero() {
		t.Fatal("running work was given an end")
	}
	// A terminal status with no end is given one: the work ended when the
	// report says it did, and "ended, unknown when" is not a state.
	ended := Record{Kind: KindAgent, Status: StatusCompleted}.WithDefaults()
	if ended.EndedAt.IsZero() {
		t.Fatal("a terminal record has no end time")
	}

	for _, bad := range []Record{
		{Kind: "browser"},
		{Kind: KindShell, Source: "guesswork"},
		{Kind: KindShell, Status: "maybe"},
		{Kind: KindShell, Input: strings.Repeat("x", MaxInputBytes+1)},
		{Kind: KindShell, Output: strings.Repeat("x", MaxOutputBytes+1)},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Validate(%+v) = %v, want ErrInvalid", bad, err)
		}
		if Code(bad.Validate()) != rpc.CodeInvalidParams {
			t.Fatalf("Code for %+v is not invalid_params", bad)
		}
	}
}

func TestStatusVocabulary(t *testing.T) {
	if !StatusCompleted.Terminal() || !StatusFailed.Terminal() || !StatusInterrupted.Terminal() {
		t.Fatal("an outcome is not terminal")
	}
	if StatusRunning.Terminal() {
		t.Fatal("running is terminal")
	}
	// The list the store applies and the question a caller asks are the same
	// vocabulary: the store keeps no copy of it, and this is what says so.
	for _, status := range TerminalStatuses() {
		if !Status(status).Terminal() {
			t.Fatalf("TerminalStatuses lists %q, which does not read as an outcome", status)
		}
	}
	if len(TerminalStatuses()) != 3 {
		t.Fatalf("TerminalStatuses = %v, want the three outcomes", TerminalStatuses())
	}
	if got := StatusForExit(0, false); got != StatusCompleted {
		t.Fatalf("exit 0 = %q", got)
	}
	if got := StatusForExit(2, false); got != StatusFailed {
		t.Fatalf("exit 2 = %q", got)
	}
	// A command the user stopped is not a command that failed, whatever code
	// the signal left behind.
	if got := StatusForExit(130, true); got != StatusInterrupted {
		t.Fatalf("interrupted = %q", got)
	}
	for outcome, want := range map[string]Status{
		"completed": StatusCompleted, "interrupted": StatusInterrupted,
		"failed": StatusFailed, "lost": StatusFailed, "something-new": StatusFailed,
	} {
		if got := StatusForOutcome(outcome); got != want {
			t.Fatalf("outcome %q = %q, want %q", outcome, got, want)
		}
	}
}

func TestWireRoundTripKeepsTheEvidence(t *testing.T) {
	started := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	code := 7
	rec, err := Decode(rpc.WorkplaceBlockInput{
		ID: "b1", Kind: "shell", Source: "engine", Input: "npm test", Status: "failed",
		RunID: "run-1", PID: 42, LogPath: "/tmp/api.log", ExitCode: &code,
		StartedAt: started.Format(time.RFC3339), EndedAt: started.Add(time.Second).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	block := BlockFromRow(rec.Row("wp"))
	block.Seq = 3
	wire := BlockWire(block)
	if wire.RunID != "run-1" || wire.PID != 42 || wire.LogPath != "/tmp/api.log" {
		t.Fatalf("the run evidence did not survive: %+v", wire)
	}
	if wire.ExitCode == nil || *wire.ExitCode != 7 || wire.EndedAt == "" {
		t.Fatalf("the outcome did not survive: %+v", wire)
	}
	if wire.StartedAt != started.Format(time.RFC3339) {
		t.Fatalf("started_at = %q, want %q", wire.StartedAt, started.Format(time.RFC3339))
	}
	// Running work has no end, and the field is absent rather than empty.
	running := BlockWire(Block{ID: "b2", Status: StatusRunning, StartedAt: started})
	if running.EndedAt != "" {
		t.Fatalf("a running block carries an end: %q", running.EndedAt)
	}
}

func TestDecodeRefusesAnUnreadableTimestamp(t *testing.T) {
	_, err := Decode(rpc.WorkplaceBlockInput{Kind: "shell", StartedAt: "yesterday"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Decode(bad time) = %v, want ErrInvalid", err)
	}
	if Code(err) != rpc.CodeInvalidParams {
		t.Fatalf("Code = %d, want invalid_params", Code(err))
	}
}

func TestCodeClassifiesTheNamedFailures(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{ErrNotFound, rpc.CodeNotFound},
		{ErrNoWorkplace, rpc.CodeNotFound},
		{ErrAmbiguous, rpc.CodeAmbiguous},
		{ErrConflict, rpc.CodeConflict},
		{ErrInvalid, rpc.CodeInvalidParams},
		{errors.New("something broke"), rpc.CodeInternal},
	}
	for _, c := range cases {
		if got := Code(c.err); got != c.want {
			t.Errorf("Code(%v) = %d, want %d", c.err, got, c.want)
		}
		if Hint(c.err) == "" {
			t.Errorf("Hint(%v) is empty", c.err)
		}
	}
	// A wrapped sentinel is still classified: callers add context with %w.
	if got := Code(fmt.Errorf("%w: spike", ErrNotFound)); got != rpc.CodeNotFound {
		t.Errorf("a wrapped ErrNotFound = %d, want not_found", got)
	}
}
