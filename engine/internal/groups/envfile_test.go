package groups

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvFileValues(t *testing.T) {
	path := filepath.Join(tempTree(t), "dev.env")
	writeFile(t, path, `# shared development settings
API_TOKEN=abc123
export DATABASE_URL=postgres://localhost:5432/app
EMPTY=
QUOTED="line one\nline two"
SINGLE='literal ${not-expanded}'
WITH_EQUALS=a=b=c
  PADDED  =  value with spaces
`)
	got, err := EnvFileValues(path)
	if err != nil {
		t.Fatalf("EnvFileValues: %v", err)
	}
	want := map[string]string{
		"API_TOKEN":    "abc123",
		"DATABASE_URL": "postgres://localhost:5432/app",
		"EMPTY":        "",
		"QUOTED":       "line one\nline two",
		"SINGLE":       "literal ${not-expanded}",
		"WITH_EQUALS":  "a=b=c",
		"PADDED":       "value with spaces",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d values, want %d: %v", len(got), len(want), got)
	}
}

func TestEnvFileValuesRejectsMalformedLinesWithoutEchoingValues(t *testing.T) {
	dir := tempTree(t)
	for _, tt := range []struct {
		name, body, want string
	}{
		{"no equals", "TOKEN secret\n", "expected KEY=VALUE"},
		{"bad key", "BAD-KEY=secret-value\n", `"BAD-KEY" is not a variable name`},
		{"unterminated quote", "TOKEN='secret-value\n", "unterminated single quote"},
		{"backslash escapes the closing quote", `TOKEN="secret-value\"` + "\n", "trailing backslash"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".env")
			writeFile(t, path, tt.body)
			_, err := EnvFileValues(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("EnvFileValues error = %v, want it to contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Errorf("error leaked the value: %v", err)
			}
		})
	}
}

func TestEnvFileValuesRejectsMissingAndNonRegularFiles(t *testing.T) {
	dir := tempTree(t)
	if _, err := EnvFileValues(filepath.Join(dir, "missing.env")); err == nil {
		t.Error("a missing env_file should be an error")
	}
	if _, err := EnvFileValues(dir); err == nil {
		t.Error("a directory should not be accepted as an env_file")
	}
}

func TestConfigEnvFilePath(t *testing.T) {
	cfg := &Config{Dir: "/work/app"}
	svc := Service{EnvFile: "../shared/dev.env"}
	if got, want := cfg.EnvFilePath(svc), "/work/shared/dev.env"; got != want {
		t.Errorf("EnvFilePath = %q, want %q", got, want)
	}
	if got := cfg.EnvFilePath(Service{EnvFile: "/etc/app.env"}); got != "/etc/app.env" {
		t.Errorf("absolute EnvFilePath = %q", got)
	}
	if got := cfg.EnvFilePath(Service{}); got != "" {
		t.Errorf("empty EnvFilePath = %q", got)
	}
}
