package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionJSONCarriesBuildIdentity(t *testing.T) {
	previous := versionJSONFlag
	versionJSONFlag = true
	t.Cleanup(func() { versionJSONFlag = previous })

	out := captureStdout(t, func() {
		if err := versionRun(testCommand(), nil); err != nil {
			t.Fatalf("versionRun: %v", err)
		}
	})

	var got versionDocument
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("version JSON: %v\n%s", err, out)
	}
	if got.Version == "" || got.Platform == "" {
		t.Fatalf("version document = %+v, want version and platform", got)
	}
}

func TestRootShortVersionFlag(t *testing.T) {
	previous := versionFlag
	versionFlag = true
	t.Cleanup(func() { versionFlag = previous })

	out := captureStdout(t, func() {
		if err := rootCmd.RunE(rootCmd, nil); err != nil {
			t.Fatalf("root version: %v", err)
		}
	})
	got := strings.TrimSpace(out)
	if got == "" || strings.Contains(got, "commit") || strings.Contains(got, "built") {
		t.Fatalf("short version output = %q, want only the CLI version", got)
	}
}
