package claudehook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
)

func TestInspectProjectSettingsIgnoresLocalFileAndDetectsHookCommand(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, ".claude")
	if err := os.MkdirAll(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(claude, "settings.local.json")
	if err := os.WriteFile(local, []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"oberth agent claude-hook"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := InspectProjectSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Present || got.Enabled {
		t.Fatalf("settings.local.json was treated as opt-in: %+v", got)
	}

	public := filepath.Join(claude, "settings.json")
	if err := os.WriteFile(public, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"oberth agent claude-hook --project /tmp/demo"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = InspectProjectSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Present || !got.Enabled || got.Path != public {
		t.Fatalf("committed settings with claude-hook not detected: %+v", got)
	}

	if err := os.WriteFile(public, []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"echo hello"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = InspectProjectSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Present || got.Enabled {
		t.Fatalf("unrelated hooks looked enabled: %+v", got)
	}
}

func TestSummarizeDeliveryCountsUnconsumedLeasesAndHidesBodies(t *testing.T) {
	home := t.TempDir()
	worktree := "0a12d03a15d322c08e082efd"
	secret := "private-finding-must-not-appear"
	writeLease(t, home, strings.Repeat("ab", 32), Lease{
		WorktreeID: worktree,
		Consumed:   false,
		Finished:   true,
		Active:     false,
		Status:     "stopped",
		Reason:     "lease_expired",
		Expires:    time.Date(2026, 11, 20, 14, 39, 26, 0, time.FixedZone("CST", 8*3600)),
		Task:       model.TaskContext{Task: secret},
		Pending:    &Feedback{Body: secret},
	})
	writeLease(t, home, strings.Repeat("cd", 32), Lease{
		WorktreeID: worktree,
		Consumed:   false,
		Finished:   false,
		Status:     "not_attached",
		Reason:     "runtime_context_unavailable",
		Expires:    time.Date(2026, 11, 20, 14, 37, 42, 0, time.FixedZone("CST", 8*3600)),
		Task:       model.TaskContext{Task: secret},
	})
	writeLease(t, home, strings.Repeat("ef", 32), Lease{
		WorktreeID: "someone-else",
		Consumed:   true,
		Status:     "stopped",
		Reason:     "lease_expired",
		Expires:    time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC),
		Pending:    &Feedback{Body: secret},
	})

	got, err := SummarizeDelivery(home, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sessions != 2 || got.Consumed != 0 || got.Unconsumed != 2 || got.Active != 0 {
		t.Fatalf("counts = %+v", got)
	}
	if got.LastStatus != "stopped" || got.LastReason != "lease_expired" {
		t.Fatalf("latest lease = %+v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), secret) || strings.Contains(got.LastReason, secret) {
		t.Fatalf("delivery view leaked lease body: %s", raw)
	}

	empty, err := SummarizeDelivery(home, "")
	if err != nil || empty.Sessions != 0 {
		t.Fatalf("empty worktree id listed leases: %+v %v", empty, err)
	}
}

func TestPublicLeaseReasonDoesNotEchoUnknownSuffixes(t *testing.T) {
	if got := publicLeaseReason("runtime_context_unavailable:timeout"); got != "runtime_context_unavailable:timeout" {
		t.Fatalf("known qualifier = %q", got)
	}
	if got := publicLeaseReason("runtime_context_unavailable:rpc exploded at /secret"); got != "runtime_context_unavailable" {
		t.Fatalf("unknown suffix leaked: %q", got)
	}
	if got := publicLeaseReason("private-text-must-not-be-shown"); got != "unavailable" {
		t.Fatalf("unknown reason = %q", got)
	}
}

func writeLease(t *testing.T, home, key string, lease Lease) {
	t.Helper()
	dir := filepath.Join(home, attn.AttentionDir, "claude", key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lease.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
