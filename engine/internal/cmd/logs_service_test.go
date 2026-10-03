package cmd

import (
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestNamedServiceLogRequiresRunOwnershipForAListener(t *testing.T) {
	path, id, empty, port := "/isolated/logs/project/api.log", "run-1", "", 8080
	for _, tc := range []struct {
		name    string
		service state.Service
		want    string
	}{
		{"managed listener", state.Service{PortActual: &port, RunID: &id, LogPath: &path}, path},
		{"unmanaged listener", state.Service{PortActual: &port, LogPath: &path}, ""},
		{"empty run identity", state.Service{PortActual: &port, RunID: &empty, LogPath: &path}, ""},
		{"managed without path", state.Service{PortActual: &port, RunID: &id}, ""},
		{"empty path", state.Service{RunID: &id, LogPath: &empty}, ""},
		{"portless", state.Service{RunID: &id, LogPath: &path}, path},
		{"finished service", state.Service{LogPath: &path, LastExit: &state.ServiceExit{RunID: id}}, path},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := preferredServiceLog(tc.service); got != tc.want {
				t.Fatalf("log = %q, want %q", got, tc.want)
			}
		})
	}
}
