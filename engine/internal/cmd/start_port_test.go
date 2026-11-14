package cmd

import (
	"reflect"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// The run id is the identity; the pid is only the fallback for a daemon that
// did not name the run. A pid that now belongs to another process must not
// borrow the old run's ports.
func TestPortsForRunUsesTheRunIdentity(t *testing.T) {
	records := []rpc.RunRecord{
		{ID: "run-1", PID: 100, Ports: []int{3000}},
		{ID: "run-2", PID: 200, Ports: []int{4000, 4001}},
	}
	cases := []struct {
		name  string
		runID string
		pid   int
		want  []int
	}{
		{"run id", "run-2", 999, []int{4000, 4001}},
		{"pid fallback", "", 100, []int{3000}},
		{"unknown run id", "missing", 100, nil},
		{"unknown pid", "", 300, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := portsForRun(records, tc.runID, tc.pid); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ports = %v, want %v", got, tc.want)
			}
		})
	}
}
