package killer

import (
	"reflect"
	"testing"
)

// npmTree is a shell wrapping pnpm, which forks vite, which forks two esbuild
// workers, plus a sibling tsgo watcher:
//
//	1 ── 100 bash ── 200 pnpm ── 300 vite ── 400 esbuild
//	                                      └─ 401 esbuild
//	                └─ 201 tsgo
func npmTree() ProcessTable {
	return ProcessTable{
		100: {PID: 100, PPID: 1, Command: "/bin/bash -c pnpm dev"},
		200: {PID: 200, PPID: 100, Command: "pnpm dev"},
		201: {PID: 201, PPID: 100, Command: "/usr/local/bin/tsgo --watch"},
		300: {PID: 300, PPID: 200, Command: "node /srv/web/node_modules/.bin/vite"},
		400: {PID: 400, PPID: 300, Command: "/srv/web/node_modules/@esbuild/bin/esbuild --service"},
		401: {PID: 401, PPID: 300, Command: "/srv/web/node_modules/@esbuild/bin/esbuild --service"},
	}
}

func TestDescendantsChildrenBeforeParents(t *testing.T) {
	got := npmTree().Descendants(100)
	want := []int{400, 401, 300, 200, 201, 100}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Descendants(100) = %v, want %v", got, want)
	}

	position := map[int]int{}
	for i, pid := range got {
		position[pid] = i
	}
	for pid, process := range npmTree() {
		parentAt, parentKnown := position[process.PPID]
		if !parentKnown {
			continue
		}
		if position[pid] > parentAt {
			t.Errorf("child %d signalled after parent %d", pid, process.PPID)
		}
	}
}

func TestDescendantsOfLeafAndUnknown(t *testing.T) {
	if order := npmTree().Descendants(400); !reflect.DeepEqual(order, []int{400}) {
		t.Errorf("leaf: got %v, want [400]", order)
	}
	// A pid the table never saw is still a single-process target.
	if order := npmTree().Descendants(999); !reflect.DeepEqual(order, []int{999}) {
		t.Errorf("unknown pid: got %v, want [999]", order)
	}
	if order := npmTree().Descendants(0); order != nil {
		t.Errorf("pid 0: got %v, want nil", order)
	}
}

func TestDescendantsSurvivesCycles(t *testing.T) {
	loopTable := ProcessTable{
		10: {PID: 10, PPID: 11},
		11: {PID: 11, PPID: 10},
	}
	order := loopTable.Descendants(10)
	if len(order) != 2 || order[len(order)-1] != 10 {
		t.Fatalf("Descendants(10) = %v, want both pids ending at the root", order)
	}
}

func TestAncestors(t *testing.T) {
	got := npmTree().Ancestors(400)
	if want := []int{300, 200, 100}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Ancestors(400) = %v, want %v", got, want)
	}
	if got := npmTree().Ancestors(100); len(got) != 0 {
		t.Fatalf("Ancestors(100) = %v, want none (pid 1 is not a signal target)", got)
	}
}

func TestName(t *testing.T) {
	table := npmTree()
	expected := map[int]string{100: "bash", 200: "pnpm", 300: "node", 400: "esbuild", 999: ""}
	for pid, name := range expected {
		if got := table.Name(pid); got != name {
			t.Errorf("Name(%d) = %q, want %q", pid, got, name)
		}
	}
}

func TestParseProcessTable(t *testing.T) {
	fixture := `
  500     1 /bin/bash -c pnpm dev
  510   500 pnpm dev
  bad line
  520   510 /srv/web/node_modules/.bin/vite --port 3300
`
	table := parseProcessTable(fixture)
	if len(table) != 3 {
		t.Fatalf("parsed %d rows, want 3: %v", len(table), table)
	}
	if row := table[520]; row.PPID != 510 ||
		row.Command != "/srv/web/node_modules/.bin/vite --port 3300" {
		t.Fatalf("row 520 = %+v", row)
	}
}
