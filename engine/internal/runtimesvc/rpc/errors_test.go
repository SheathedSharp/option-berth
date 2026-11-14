package rpc

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestAnErrorCarriesItsNameAndHint(t *testing.T) {
	failure := NewError(CodeNotFound, "port 3000 not found", "run `oberth status`")
	if failure.Code != 1001 || failure.Data.Code != "not_found" || failure.Data.Hint == "" {
		t.Fatalf("%+v", failure)
	}
	if failure.Error() != "port 3000 not found" {
		t.Errorf("Error() = %q", failure.Error())
	}
}

// Every declared number answers with a name, and a number the vocabulary does
// not hold is a fault rather than an empty string: a client must always have a
// branch.
func TestEveryFailureAnswersWithANameAndAKind(t *testing.T) {
	for _, entry := range failures {
		if CodeName(entry.number) != entry.name {
			t.Errorf("CodeName(%d) = %q, want %q", entry.number, CodeName(entry.number), entry.name)
		}
		if KindOf(entry.number) != entry.kind {
			t.Errorf("KindOf(%d) = %v, want %v", entry.number, KindOf(entry.number), entry.kind)
		}
	}
	if got := CodeName(4242); got != "internal" {
		t.Errorf("an unknown number answers %q, want internal", got)
	}
	if got := KindOf(4242); got != Fault {
		t.Errorf("an unknown number is %v, want Fault", got)
	}
}

// The vocabulary is the contract. Two directions, two different answers.
//
// The contract may not name a code the runtime service cannot return: that is a promise
// with no producer, and it is how "timeout" survived here long after readiness
// expiry became a service state. That direction is absolute.
func TestTheContractNamesNoCodeTheRuntimeServiceCannotReturn(t *testing.T) {
	declared := map[string]bool{}
	for _, name := range FailureNames() {
		declared[name] = true
	}
	for _, name := range documentedCodes(t) {
		if !declared[name] {
			t.Errorf("docs/cli.md promises %q, which the runtime service never returns", name)
		}
	}
}

// undocumented is the part of the vocabulary docs/cli.md does not name yet.
//
// These are real failures the runtime service returns today. They are not in the
// contract's sentence because that sentence is checked against the agent skill
// (scripts/check_skill_contract.py), and the skill is a separate product that
// has to cover a code before the engine may advertise it. Until it does, this
// list is the gap; the test below keeps it from changing quietly in either
// direction.
var undocumented = []string{
	"claim_conflict",
	"conflict",
	"invalid_selector",
	"outside_home",
	"session_not_found",
	"unsupported_platform",
}

func TestTheUndocumentedGapIsExactlyWhatIsRecorded(t *testing.T) {
	named := map[string]bool{}
	for _, name := range documentedCodes(t) {
		named[name] = true
	}
	var gap []string
	for _, name := range FailureNames() {
		if !named[name] {
			gap = append(gap, name)
		}
	}
	sort.Strings(gap)
	want := append([]string(nil), undocumented...)
	sort.Strings(want)
	if strings.Join(gap, ",") != strings.Join(want, ",") {
		t.Fatalf("the gap between the vocabulary and docs/cli.md changed\n  new:     %v\n  recorded: %v\nUpdate the contract and the skill, then this list.",
			minus(gap, want), minus(want, gap))
	}
}

// documentedCodes reads the stable-code sentence out of docs/cli.md. The test
// reads the English side because that is the authoritative one; the Chinese
// pair is checked by scripts/check_docs.py for structure, and by the docs' own
// rule for meaning.
func documentedCodes(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "docs", "cli.md")
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	match := regexp.MustCompile(`The stable codes are ([^.]*)\.`).FindStringSubmatch(string(text))
	if match == nil {
		t.Fatal("docs/cli.md has no sentence listing the stable error codes")
	}
	var codes []string
	for _, token := range regexp.MustCompile(`,|\band\b`).Split(match[1], -1) {
		if token = strings.TrimSpace(token); regexp.MustCompile(`^[a-z][a-z0-9_]+$`).MatchString(token) {
			codes = append(codes, token)
		}
	}
	if len(codes) == 0 {
		t.Fatal("the stable-code sentence names no codes")
	}
	return codes
}

// minus is the elements of a that b does not hold.
func minus(a, b []string) []string {
	held := make(map[string]bool, len(b))
	for _, value := range b {
		held[value] = true
	}
	var out []string
	for _, value := range a {
		if !held[value] {
			out = append(out, value)
		}
	}
	return out
}

// The observation codes are the ones an agent reads to decide whether it may
// trust an "absent listener" answer, so their numbers and kinds are pinned.
func TestObservationFailuresAreConditions(t *testing.T) {
	want := map[int]string{
		CodeObservationEmpty:        "observation_empty",
		CodeObservationIncomplete:   "observation_incomplete",
		CodeObservationUnrecognized: "observation_unrecognized",
		CodeObservationBlind:        "observation_blind",
		CodeObservationBindDenied:   "observation_bind_denied",
	}
	for code, name := range want {
		if got := CodeName(code); got != name {
			t.Errorf("CodeName(%d) = %q, want %q", code, got, name)
		}
		if KindOf(code) != Condition {
			t.Errorf("%s is %v, want a condition", name, KindOf(code))
		}
	}
}

// The numbers the retired share capability held are gone, not reserved: this
// engine never returned them, and a client cannot have cached a code it was
// never sent.
func TestTheRetiredShareNumbersAreUnused(t *testing.T) {
	for _, code := range []int{1100, 1107, 1108, 1110, 1111, 1112, 1113, 1114} {
		if _, held := vocabulary[code]; held {
			t.Errorf("code %d is still in the vocabulary", code)
		}
	}
}

// A failure the runtime service can report must be one a caller can act on, so the table
// holds no entry without a kind and no duplicate number or name.
func TestTheVocabularyIsWellFormed(t *testing.T) {
	numbers := map[int]bool{}
	names := map[string]bool{}
	for _, entry := range failures {
		if entry.number == 0 {
			t.Errorf("%s has no wire number", entry.name)
		}
		if numbers[entry.number] {
			t.Errorf("%d is declared twice", entry.number)
		}
		if names[entry.name] {
			t.Errorf("%q is declared twice", entry.name)
		}
		if entry.kind != Refusal && entry.kind != Condition && entry.kind != Fault {
			t.Errorf("%s has no usable kind (%v)", entry.name, entry.kind)
		}
		numbers[entry.number] = true
		names[entry.name] = true
	}
}
