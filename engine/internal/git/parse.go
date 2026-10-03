package git

import (
	"strconv"
	"strings"
)

// parseStatus reads `git status --porcelain=v2 --branch -z`.
//
// The format (git-status(1), "Porcelain Format Version 2"):
//
//	# branch.oid <commit> | (initial)
//	# branch.head <branch> | (detached)
//	# branch.upstream <upstream_branch>
//	# branch.ab +<ahead> -<behind>
//	1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
//	2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path> NUL <origPath> NUL
//	u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
//	? <path>
//	! <path>
//
// With -z every record ends in NUL — paths keep their spaces, unquoted — and a
// renamed record's original path is the record right after it. Verified against
// git 2.50.1 rather than inferred, because the sister-record rule is the one
// thing the non-z output never shows.
//
// It fills in the counts and returns the rows those counts are made of: one
// call, one walk, and the two cannot drift apart.
func parseStatus(out []byte, s *Snapshot) []File {
	recs := splitNUL(out)
	files := []File{}
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		switch {
		case strings.HasPrefix(rec, "# branch.oid "):
			if oid := strings.TrimPrefix(rec, "# branch.oid "); oid != "(initial)" {
				s.Head = oid
			}
		case strings.HasPrefix(rec, "# branch.head "):
			if head := strings.TrimPrefix(rec, "# branch.head "); head == "(detached)" {
				s.Detached = true
			} else {
				s.Branch = head
			}
		case strings.HasPrefix(rec, "# branch.upstream "):
			s.Upstream = strings.TrimPrefix(rec, "# branch.upstream ")
		case strings.HasPrefix(rec, "# branch.ab "):
			parseAheadBehind(strings.TrimPrefix(rec, "# branch.ab "), s)
		case strings.HasPrefix(rec, "1 "):
			xy := field(rec, 1)
			countChanges(xy, s)
			files = append(files, File{Path: pathField(rec, 8), Status: shortStatus(xy)})
		case strings.HasPrefix(rec, "2 "):
			xy := field(rec, 1)
			countChanges(xy, s)
			renamed := File{Path: pathField(rec, 9), Status: shortStatus(xy)}
			if i+1 < len(recs) {
				i++ // the original path is a record of its own
				renamed.OldPath = recs[i]
			}
			files = append(files, renamed)
		case strings.HasPrefix(rec, "u "):
			s.Conflicts++
			files = append(files, File{Path: pathField(rec, 10), Status: shortStatus(field(rec, 1))})
		case strings.HasPrefix(rec, "? "):
			s.Untracked++
			files = append(files, File{Path: rec[2:], Status: "??"})
		}
	}
	return files
}

// shortStatus turns v2's pair into the short-format pair a person reads in
// `git status --short`: v2 writes "." where nothing moved in that column, the
// short form writes a space. Same two letters, the vocabulary everyone knows.
func shortStatus(xy string) string {
	if len(xy) != 2 {
		return xy
	}
	return strings.ReplaceAll(xy, ".", " ")
}

// numstat is one file's line counts from `git diff --numstat -z`.
type numstat struct {
	additions int
	deletions int
	binary    bool
}

// parseNumstat reads `git diff --numstat -z`: <additions> TAB <deletions> TAB
// <path>, where "-" on either side means binary.
//
// A rename has no path in that field — measured on git 2.50.1: the field is
// empty and the old and the new path follow as records of their own, in that
// order. The map is keyed by the new path, which is the one a status row
// carries.
func parseNumstat(out []byte) map[string]numstat {
	recs := splitNUL(out)
	stats := map[string]numstat{}
	for i := 0; i < len(recs); i++ {
		parts := strings.SplitN(recs[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" {
			if i+2 >= len(recs) {
				continue
			}
			path = recs[i+2]
			i += 2
		}
		add, addErr := strconv.Atoi(parts[0])
		del, delErr := strconv.Atoi(parts[1])
		if addErr != nil || delErr != nil {
			stats[path] = numstat{binary: true}
			continue
		}
		stats[path] = numstat{additions: add, deletions: del}
	}
	return stats
}

// countChanges reads the XY field: X is the staged status, Y the unstaged one,
// and "." means "unchanged in that respect". A file with both counts once in
// each — that is what a person means by "two files need looking at".
func countChanges(xy string, s *Snapshot) {
	if len(xy) < 2 {
		return
	}
	if xy[0] != '.' {
		s.Staged++
	}
	if xy[1] != '.' {
		s.Unstaged++
	}
}

// parseAheadBehind reads "+<ahead> -<behind>", which git only prints when the
// branch has an upstream: no upstream means neither number is knowable, and
// zero is the honest answer.
func parseAheadBehind(s string, out *Snapshot) {
	for _, part := range strings.Fields(s) {
		switch {
		case strings.HasPrefix(part, "+"):
			out.Ahead, _ = strconv.Atoi(part[1:])
		case strings.HasPrefix(part, "-"):
			out.Behind, _ = strconv.Atoi(part[1:])
		}
	}
}

// parseWorktrees reads `git worktree list --porcelain -z`: one block per
// worktree, each opening with "worktree <path>", blocks separated by an empty
// record.
func parseWorktrees(out []byte) []Worktree {
	var list []Worktree
	for _, rec := range splitNUL(out) {
		if strings.HasPrefix(rec, "worktree ") {
			list = append(list, Worktree{Path: strings.TrimPrefix(rec, "worktree ")})
			continue
		}
		if len(list) == 0 {
			continue // nothing has opened a block yet
		}
		cur := &list[len(list)-1]
		switch {
		case strings.HasPrefix(rec, "HEAD "):
			cur.Head = strings.TrimPrefix(rec, "HEAD ")
		case strings.HasPrefix(rec, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(rec, "branch "), "refs/heads/")
		case rec == "detached":
			cur.Detached = true
		case rec == "bare":
			cur.Bare = true
		}
	}
	return list
}

// parseCommit reads `git log -1 --format=%h%x00%s%x00%cI`.
func parseCommit(out []byte) *Commit {
	parts := strings.Split(strings.TrimRight(string(out), "\n"), "\x00")
	if len(parts) < 2 || parts[0] == "" {
		return nil
	}
	c := &Commit{Hash: parts[0], Subject: parts[1]}
	if len(parts) > 2 {
		c.When = parts[2]
	}
	return c
}

// field returns the nth space-separated field of a porcelain record: one token,
// for the fixed-width columns at the head of a record.
func field(rec string, n int) string {
	parts := strings.SplitN(rec, " ", n+2)
	if len(parts) <= n {
		return ""
	}
	return parts[n]
}

// pathField returns the path of a porcelain record: everything from the nth
// space on. A path is a single field and it may contain spaces, so it must not
// be split like the columns before it — measured against a file named
// "with space.txt", which `field` quietly cut down to "with".
func pathField(rec string, n int) string {
	parts := strings.SplitN(rec, " ", n+1)
	if len(parts) <= n {
		return ""
	}
	return parts[n]
}

// splitNUL breaks a -z stream into records. The trailing NUL leaves a final
// empty element, which is not a record; interior empties are the separators
// between output blocks and are left for the callers to skip.
func splitNUL(b []byte) []string {
	parts := strings.Split(string(b), "\x00")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
