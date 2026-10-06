package git

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// graphTimeout bounds a bounded history read. A graph is deliberately a
// separate read from status/files/diff so callers can request history without
// making the normal worktree review slower.
const graphTimeout = 12 * time.Second

const (
	defaultGraphLimit = 200
	maxGraphLimit     = 500
)

// Graph is a bounded, read-only commit topology. observed_head pins the HEAD
// observed by the same status read that discovers the repository root; callers
// must not present an older graph as the current checkout after navigation.
type Graph struct {
	Root         string        `json:"root"`
	ObservedHead string        `json:"observed_head,omitempty"`
	Branch       string        `json:"branch,omitempty"`
	Detached     bool          `json:"detached,omitempty"`
	Upstream     string        `json:"upstream,omitempty"`
	Ahead        int           `json:"ahead"`
	Behind       int           `json:"behind"`
	Truncated    bool          `json:"truncated"`
	Limit        int           `json:"limit"`
	Commits      []GraphCommit `json:"commits"`
	Refs         []GraphRef    `json:"refs"`
	Worktrees    []Worktree    `json:"worktrees"`
}

// GraphCommit is one commit returned by the bounded log. Parents are full
// object IDs so a client can draw merge lines without re-reading Git.
type GraphCommit struct {
	Hash    string   `json:"hash"`
	Parents []string `json:"parents,omitempty"`
	Author  string   `json:"author,omitempty"`
	When    string   `json:"when,omitempty"`
	Subject string   `json:"subject"`
	Refs    []string `json:"refs,omitempty"`
}

// GraphRef is a local ref observed during the graph read. Kind is one of
// branch, remote, tag, or ref; current identifies refs pointing at observed
// HEAD without claiming that the ref moved atomically with the log.
type GraphRef struct {
	Name    string `json:"name"`
	Target  string `json:"target"`
	Kind    string `json:"kind"`
	Current bool   `json:"current,omitempty"`
}

// ReadGraph reads a bounded commit graph and local refs. It never fetches,
// refreshes the index, enables fsmonitor, or writes repository state.
func ReadGraph(ctx context.Context, dir string, limit int) (Graph, error) {
	root, _, ok := groups.Find(dir)
	if !ok {
		return Graph{}, ErrNotARepository
	}
	if limit == 0 {
		limit = defaultGraphLimit
	}
	if limit < 1 || limit > maxGraphLimit {
		return Graph{}, fmt.Errorf("graph limit must be between 1 and %d", maxGraphLimit)
	}
	ctx, cancel := context.WithTimeout(ctx, graphTimeout)
	defer cancel()

	snap, _, err := readStatus(ctx, dir)
	if err != nil {
		return Graph{}, err
	}
	snap.Root = root
	if err := readRest(ctx, dir, &snap); err != nil {
		return Graph{}, err
	}

	logArgs := []string{"log"}
	if snap.Head != "" {
		// Put the observed HEAD first even when --all adds other refs. The
		// client can then reject a response whose first row no longer pins the
		// status observation.
		logArgs = append(logArgs, snap.Head)
	}
	logArgs = append(logArgs, "--all", "--date-order", "--topo-order",
		fmt.Sprintf("--max-count=%d", limit+1), "--format=%H%x00%P%x00%an%x00%cI%x00%s%x00")
	logOut, err := gitOut(ctx, dir, logArgs...)
	if err != nil {
		return Graph{}, err
	}
	commits := parseGraphCommits(logOut)
	if snap.Head != "" && (len(commits) == 0 || commits[0].Hash != snap.Head) {
		return Graph{}, fmt.Errorf("git HEAD changed during graph read")
	}
	truncated := len(commits) > limit
	if truncated {
		commits = commits[:limit]
	}

	refOut, err := gitOut(ctx, dir, "for-each-ref", "--format=%(objectname)%x00%(*objectname)%x00%(refname)%x00")
	if err != nil {
		return Graph{}, err
	}
	refs := parseGraphRefs(refOut, snap.Head)
	byHash := map[string][]string{}
	for _, ref := range refs {
		byHash[ref.Target] = append(byHash[ref.Target], ref.Name)
	}
	for i := range commits {
		commits[i].Refs = byHash[commits[i].Hash]
	}

	return Graph{Root: root, ObservedHead: snap.Head, Branch: snap.Branch, Detached: snap.Detached,
		Upstream: snap.Upstream, Ahead: snap.Ahead, Behind: snap.Behind, Truncated: truncated,
		Limit: limit, Commits: commits, Refs: refs, Worktrees: snap.Worktrees}, nil
}

func parseGraphCommits(out []byte) []GraphCommit {
	records := splitNUL(out)
	commits := make([]GraphCommit, 0, len(records)/5)
	for i := 0; i+4 < len(records); i += 5 {
		hash := strings.TrimSpace(records[i])
		if hash == "" || len(hash) < 7 {
			continue
		}
		parents := strings.Fields(records[i+1])
		commits = append(commits, GraphCommit{Hash: hash, Parents: parents,
			Author: records[i+2], When: records[i+3], Subject: records[i+4]})
	}
	return commits
}

func parseGraphRefs(out []byte, observedHead string) []GraphRef {
	records := splitNUL(out)
	refs := make([]GraphRef, 0, len(records)/3)
	for i := 0; i+2 < len(records); i += 3 {
		target, peeled, full := strings.TrimSpace(records[i]), strings.TrimSpace(records[i+1]), strings.TrimSpace(records[i+2])
		if peeled != "" {
			target = peeled
		}
		if target == "" || full == "" {
			continue
		}
		name := full
		kind := "ref"
		switch {
		case strings.HasPrefix(full, "refs/heads/"):
			name, kind = strings.TrimPrefix(full, "refs/heads/"), "branch"
		case strings.HasPrefix(full, "refs/remotes/"):
			name, kind = strings.TrimPrefix(full, "refs/remotes/"), "remote"
		case strings.HasPrefix(full, "refs/tags/"):
			name, kind = strings.TrimPrefix(full, "refs/tags/"), "tag"
		}
		refs = append(refs, GraphRef{Name: name, Target: target, Kind: kind, Current: target == observedHead})
	}
	return refs
}
