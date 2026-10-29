package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestVersionValueFallsBackToModuleVersion(t *testing.T) {
	installed := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, true
	}
	devel := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	}
	missing := func() (*debug.BuildInfo, bool) { return nil, false }

	for _, test := range []struct {
		name    string
		stamped string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{name: "linked version wins", stamped: "1.2.3", read: installed, want: "v1.2.3"},
		{name: "linked v prefix is kept", stamped: "v1.2.3", read: installed, want: "v1.2.3"},
		{name: "go install falls back to module version", stamped: "dev", read: installed, want: "v1.2.3"},
		{name: "go install normalizes bare module version", stamped: "dev", read: func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: "1.2.3"}}, true
		}, want: "v1.2.3"},
		{name: "development build keeps dev", stamped: "dev", read: devel, want: "dev"},
		{name: "missing build info keeps dev", stamped: "dev", read: missing, want: "dev"},
		{name: "unknown stamp can be replaced", stamped: "unknown", read: installed, want: "v1.2.3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := versionValue(test.stamped, test.read); got != test.want {
				t.Fatalf("versionValue(%q) = %q, want %q", test.stamped, got, test.want)
			}
		})
	}
}
