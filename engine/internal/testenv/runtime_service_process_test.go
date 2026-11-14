package testenv

import "testing"

func TestRuntimeServiceProcessBoundary(t *testing.T) {
	for _, tc := range []struct {
		line, exe string
		ok        bool
	}{
		{"/tmp/my bin/oberth runtime service run --detach", "/tmp/my bin/oberth", true},
		{"/tmp/x/oberth runtime service run", "/tmp/x/oberth", true},
		{"/tmp/x/oberth serve --detach", "", false},
		{"/tmp/x/oberth daemon status", "", false},
		{"/tmp/x/oberth runtime service runall", "", false},
		{"/tmp/x/oberth runtime service status", "", false},
		{"/tmp/a runtime service runner/oberth runtime service run", "/tmp/a runtime service runner/oberth", true},
	} {
		exe, ok := splitRuntimeServiceRun(tc.line)
		if exe != tc.exe || ok != tc.ok {
			t.Fatalf("%q -> %q,%v; want %q,%v", tc.line, exe, ok, tc.exe, tc.ok)
		}
	}
}
