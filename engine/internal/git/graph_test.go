package git

import "testing"

func TestParseGraphCommitsKeepsParentsAndFields(t *testing.T) {
	out := []byte("head\x00parent other\x00Ada\x002026-10-06T00:00:00Z\x00merge subject\x00parent\x00\x00Bob\x002026-10-05T00:00:00Z\x00root\x00")
	commits := parseGraphCommits(out)
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2: %+v", len(commits), commits)
	}
	if commits[0].Hash != "head" || len(commits[0].Parents) != 2 || commits[0].Parents[1] != "other" {
		t.Fatalf("first commit = %+v, want full parent list", commits[0])
	}
	if commits[1].Author != "Bob" || commits[1].Subject != "root" {
		t.Fatalf("second commit = %+v, fields were not preserved", commits[1])
	}
}

func TestParseGraphRefsClassifiesAndMarksCurrent(t *testing.T) {
	out := []byte("head\x00\x00refs/heads/main\x00head\x00\x00refs/remotes/origin/main\x00tag-object\x00head\x00refs/tags/v1\x00")
	refs := parseGraphRefs(out, "head", "main")
	if len(refs) != 3 {
		t.Fatalf("got %d refs, want 3: %+v", len(refs), refs)
	}
	if refs[0].Name != "main" || refs[0].Kind != "branch" || !refs[0].Current {
		t.Errorf("branch ref = %+v", refs[0])
	}
	if refs[1].Name != "origin/main" || refs[1].Kind != "remote" || refs[1].Current {
		t.Errorf("remote ref = %+v", refs[1])
	}
	if refs[2].Name != "v1" || refs[2].Kind != "tag" {
		t.Errorf("tag ref = %+v", refs[2])
	}
}
