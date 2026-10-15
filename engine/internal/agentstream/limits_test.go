package agentstream

import "testing"

func TestClipWithBudget(t *testing.T) {
	budget := 10
	out, truncated := clipWithBudget("hello world", 100, &budget)
	if out != "hello worl" || !truncated || budget != 0 {
		t.Fatalf("unexpected clip: out=%q truncated=%v budget=%d", out, truncated, budget)
	}
}
