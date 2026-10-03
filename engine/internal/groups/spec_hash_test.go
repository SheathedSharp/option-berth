package groups

import "testing"

func TestServiceSpecHashExcludesEnvironmentValues(t *testing.T) {
	a := Service{Name: "api", Cmd: "go run .", Port: 8080, Env: map[string]string{"TOKEN": "one"}}
	b := a
	b.Env = map[string]string{"TOKEN": "two"}
	if ServiceSpecHash(a) != ServiceSpecHash(b) {
		t.Fatal("spec hash changed with an environment value")
	}
	b.Cmd = "go run ./cmd/api"
	if ServiceSpecHash(a) == ServiceSpecHash(b) {
		t.Fatal("spec hash did not change with the command")
	}
}
