package draft

import (
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

func TestBriefCarriesTheFacts(t *testing.T) {
	b := Brief(Facts{
		Root:  "/Users/me/code/demo",
		Group: "demo",
		CLI:   "claude",
		Candidates: []groups.Candidate{
			{Port: 8000, Process: "api", Cwd: "backend", PID: 42, Command: "node   server.js\n--port 8000"},
		},
		Existing: "name: demo\nservices: []\n",
	})
	for _, want := range []string{
		"/Users/me/code/demo",    // the root
		"worktree name: demo",    // the worktree
		"8000", "api", "backend", // the candidate
		"pid 42",                     // identity
		"node server.js --port 8000", // the command line, whitespace collapsed
		"name: demo",                 // the existing file is quoted back
		"oberth.yaml",                // the rules are in front of it
		"One service is one foreground process",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("the brief does not mention %q:\n%s", want, b)
		}
	}
}

func TestBriefWithoutCandidatesSaysSo(t *testing.T) {
	b := Brief(Facts{Root: "/x", Group: "x", CLI: "codex"})
	if !strings.Contains(b, "Nothing is listening") {
		t.Errorf("a brief with no evidence should say so:\n%s", b)
	}
	if strings.Contains(b, "## Listening") {
		t.Errorf("a brief with no evidence printed the listening section:\n%s", b)
	}
	if !strings.Contains(b, "There is no oberth.yaml yet") {
		t.Errorf("a brief with no manifest should say so:\n%s", b)
	}
}

func TestBriefCapsTheCandidateList(t *testing.T) {
	cands := make([]groups.Candidate, 0, maxCandidates+3)
	for i := 0; i < maxCandidates+3; i++ {
		cands = append(cands, groups.Candidate{Port: 10000 + i, Process: "p"})
	}
	b := Brief(Facts{Root: "/x", Group: "x", CLI: "claude", Candidates: cands})
	if !strings.Contains(b, "and 3 more listeners") {
		t.Errorf("the brief should name what it left out:\n%s", b)
	}
}
