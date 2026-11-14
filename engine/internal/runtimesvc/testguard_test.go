package runtimesvc

import "testing"

func TestIsTestBinary(t *testing.T) {
	cases := []struct {
		path string
		test bool
	}{
		{path: "/tmp/go-build123/b001/cmd.test", test: true},
		{path: "/tmp/go-build123/b001/runtime.service.test", test: true},
		{path: `C:\Temp\go-build\b001\cmd.test.exe`, test: true},
		{path: "cmd.test", test: true},
		{path: "/usr/local/bin/oberth", test: false},
		{path: `C:\Program Files\option-berth\oberth.exe`, test: false},
		{path: "/tmp/option-berth-itest-bin123/option-berth", test: false},
		{path: "/home/dev/testing/option-berth", test: false},
		{path: "/home/dev/option-berth.test.backup/option-berth", test: false},
	}
	for _, tc := range cases {
		if got := IsTestBinary(tc.path); got != tc.test {
			t.Errorf("IsTestBinary(%q) = %v, want %v", tc.path, got, tc.test)
		}
	}
}

func TestEnvEnabled(t *testing.T) {
	cases := []struct {
		value string
		on    bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{" FALSE ", false},
		{"1", true},
		{"true", true},
		{"yes", true},
		{"anything", true},
	}
	for _, tc := range cases {
		if got := EnvEnabled(tc.value); got != tc.on {
			t.Errorf("EnvEnabled(%q) = %v, want %v", tc.value, got, tc.on)
		}
	}
}

func TestTestRuntimeServiceAllowedFollowsTheEnvironment(t *testing.T) {
	if TestRuntimeServiceAllowed() {
		t.Fatalf("%s is set during a test run; the guard is disarmed", AllowTestRuntimeServiceEnv)
	}
	t.Setenv(AllowTestRuntimeServiceEnv, "1")
	if !TestRuntimeServiceAllowed() {
		t.Error("setting the opt-in variable explicitly did not arm the guard")
	}
}
