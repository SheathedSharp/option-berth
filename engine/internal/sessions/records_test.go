package sessions

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func moment(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("bad fixture time %q: %v", value, err)
	}
	return parsed
}

func identity(id, tool string) state.Session {
	return state.Session{ID: id, Tool: tool, Worktree: "feature-x", Branch: "feature/x"}
}

func listening(port int, group string, owner *state.Session) state.Port {
	row := state.Port{Port: port, BindAddress: "127.0.0.1", PID: 2000 + port, Session: owner}
	if group != "" {
		named := group
		row.Group = &named
	}
	return row
}

func requireRecord(t *testing.T, records []state.SessionRecord, id string) state.SessionRecord {
	t.Helper()
	for _, record := range records {
		if record.ID == id {
			return record
		}
	}
	t.Fatalf("no record %q among %+v", id, records)
	return state.SessionRecord{}
}

func TestRecordsJoinRunsPortsAndGroups(t *testing.T) {
	claude := identity("s1", ToolClaudeCode)
	codex := identity("s2", ToolCodex)
	live := []Live{
		{RunID: "r1", PID: 11, Group: "web", Session: claude, StartedAt: moment(t, "2026-09-01T10:00:00Z")},
		{RunID: "r2", PID: 12, Group: "api", Session: claude, StartedAt: moment(t, "2026-09-01T10:01:00Z")},
	}
	stored := []Known{
		{Session: claude, FirstSeen: moment(t, "2026-09-01T09:00:00Z"), LastSeen: moment(t, "2026-09-01T10:01:00Z")},
		{Session: codex, FirstSeen: moment(t, "2026-08-30T09:00:00Z"), LastSeen: moment(t, "2026-08-30T09:30:00Z")},
	}
	ports := []state.Port{
		listening(3000, "web", &claude),
		listening(3001, "api", &claude),
		listening(5432, "db", nil),
	}

	records := Records(stored, live, ports)
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(records), records)
	}

	active := requireRecord(t, records, "s1")
	if active.Runs != 2 || active.Ports != 2 || active.Groups != 2 {
		t.Errorf("s1 counts: runs=%d ports=%d groups=%d, want 2/2/2",
			active.Runs, active.Ports, active.Groups)
	}
	if !active.Active {
		t.Error("s1 has live runs but reads inactive")
	}
	if active.FirstSeen != "2026-09-01T09:00:00Z" {
		t.Errorf("s1 first_seen = %q", active.FirstSeen)
	}

	inactive := requireRecord(t, records, "s2")
	if inactive.Active || inactive.Runs != 0 || inactive.Ports != 0 {
		t.Errorf("s2 = %+v, want no runs, no ports and inactive", inactive)
	}
	if records[0].ID != "s1" {
		t.Errorf("active session is not listed first: %+v", records)
	}
}

func TestPortsIntroduceSessionsTheStoreNeverSaw(t *testing.T) {
	ghost := identity("ghost", ToolCursor)
	records := Records(nil, nil, []state.Port{listening(4000, "web", &ghost)})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0].Ports != 1 || !records[0].Active {
		t.Errorf("record = %+v", records[0])
	}
}

func TestLiveIdentityRefreshesTheStoredOne(t *testing.T) {
	stored := state.Session{ID: "s1", Tool: ToolClaudeCode, Branch: "old"}
	live := state.Session{ID: "s1", Tool: ToolClaudeCode, Branch: "new"}
	records := Records(
		[]Known{{Session: stored, LastSeen: moment(t, "2026-09-01T10:00:00Z")}},
		[]Live{{RunID: "r1", Session: live}},
		nil,
	)
	if records[0].Branch != "new" {
		t.Errorf("branch = %q, want the live run's", records[0].Branch)
	}
}

func TestRecordsSkipSessionsWithoutIdentity(t *testing.T) {
	empty := state.Session{Tool: ToolAgent}
	records := Records(
		[]Known{{Session: empty}, {Session: state.Session{ID: "real", Tool: ToolAgent}}},
		[]Live{{RunID: "r1", Session: empty}},
		[]state.Port{listening(3000, "web", &empty)},
	)
	if len(records) != 1 || records[0].ID != "real" {
		t.Fatalf("records = %+v, want only the named session", records)
	}
}

func TestRecordsCountDistinctGroupsOnly(t *testing.T) {
	owner := identity("s1", ToolClaudeCode)
	records := Records(nil,
		[]Live{
			{RunID: "r1", Group: "web", Session: owner},
			{RunID: "r2", Group: "web", Session: owner},
		},
		[]state.Port{listening(3000, "web", &owner), listening(3001, "db", &owner)},
	)
	if len(records) != 1 || records[0].Groups != 2 {
		t.Errorf("groups = %d, want 2 distinct names", records[0].Groups)
	}
}

func TestRecordsOrderIsStableAndNewestFirst(t *testing.T) {
	a := identity("a", ToolAgent)
	b := identity("b", ToolAgent)
	c := identity("c", ToolAgent)
	records := Records([]Known{
		{Session: b, LastSeen: moment(t, "2026-09-01T12:00:00Z")},
		{Session: a, LastSeen: moment(t, "2026-09-01T12:00:00Z")},
		{Session: c, LastSeen: moment(t, "2026-09-02T12:00:00Z")},
	}, nil, nil)
	got := []string{records[0].ID, records[1].ID, records[2].ID}
	want := []string{"c", "a", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestRunsOfAndPortsOf(t *testing.T) {
	claude := identity("s1", ToolClaudeCode)
	codex := identity("s2", ToolCodex)
	live := []Live{
		{RunID: "late", Session: claude, StartedAt: moment(t, "2026-09-01T11:00:00Z")},
		{RunID: "early", Session: claude, StartedAt: moment(t, "2026-09-01T10:00:00Z")},
		{RunID: "other", Session: codex},
	}
	runs := RunsOf(live, "s1")
	if len(runs) != 2 || runs[0].RunID != "early" {
		t.Errorf("RunsOf = %+v, want early before late", runs)
	}

	ports := PortsOf([]state.Port{
		listening(3000, "", &claude),
		listening(3001, "", &codex),
	}, "s2")
	if len(ports) != 1 || ports[0].Port != 3001 {
		t.Errorf("PortsOf = %+v", ports)
	}
	if PortsOf(nil, "s1") == nil {
		t.Error("PortsOf returned nil instead of an empty slice")
	}
}

func TestRetentionIsSevenDays(t *testing.T) {
	if Retention != 7*24*time.Hour {
		t.Errorf("Retention = %v", Retention)
	}
}
