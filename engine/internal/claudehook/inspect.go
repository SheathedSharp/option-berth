package claudehook

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
)

// ProjectSettings reports whether a worktree's committed Claude settings
// enable oberth's hook command. It never reads settings.local.json and never
// writes a file.
type ProjectSettings struct {
	Path    string `json:"path,omitempty"`
	Present bool   `json:"settings_present"`
	Enabled bool   `json:"enabled"`
}

// DeliveryView is a redacted count of hook delivery leases for one worktree.
// It never includes pending findings, task text, or unknown reason strings.
type DeliveryView struct {
	Sessions   int    `json:"sessions"`
	Consumed   int    `json:"consumed"`
	Unconsumed int    `json:"unconsumed"`
	Active     int    `json:"active"`
	LastStatus string `json:"last_status,omitempty"`
	LastReason string `json:"last_reason,omitempty"`
}

// InspectProjectSettings looks only at <root>/.claude/settings.json.
func InspectProjectSettings(root string) (ProjectSettings, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return ProjectSettings{}, nil
	}
	if !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return ProjectSettings{}, err
		}
		root = abs
	}
	path := filepath.Join(root, ".claude", "settings.json")
	view := ProjectSettings{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return view, nil
		}
		return view, err
	}
	view.Present = true
	var payload any
	if json.Unmarshal(data, &payload) != nil {
		return view, nil
	}
	view.Enabled = jsonContainsClaudeHook(payload)
	return view, nil
}

func jsonContainsClaudeHook(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for _, item := range v {
			if jsonContainsClaudeHook(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if jsonContainsClaudeHook(item) {
				return true
			}
		}
	case string:
		return strings.Contains(v, "claude-hook")
	}
	return false
}

// SummarizeDelivery lists lease.json files under the Attention hook directory
// that belong to worktreeID. Unknown keys, other worktrees, and lease bodies
// are ignored; Pending and Task are never copied into the view.
func SummarizeDelivery(home, worktreeID string) (DeliveryView, error) {
	var view DeliveryView
	worktreeID = strings.TrimSpace(worktreeID)
	home = filepath.Clean(strings.TrimSpace(home))
	if worktreeID == "" || home == "" || !filepath.IsAbs(home) {
		return view, nil
	}
	dir := filepath.Join(home, attn.AttentionDir, "claude")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return view, nil
		}
		return view, err
	}
	var latest time.Time
	for _, entry := range entries {
		if !entry.IsDir() || !validKey(entry.Name()) {
			continue
		}
		lease, err := readDeliveryLease(filepath.Join(dir, entry.Name(), "lease.json"))
		if err != nil || lease.WorktreeID != worktreeID {
			continue
		}
		view.Sessions++
		if lease.Consumed {
			view.Consumed++
		} else {
			view.Unconsumed++
		}
		if lease.Active {
			view.Active++
		}
		if latest.IsZero() || lease.Expires.After(latest) {
			latest = lease.Expires
			view.LastStatus = publicLeaseStatus(lease.Status)
			view.LastReason = publicLeaseReason(lease.Reason)
		}
	}
	return view, nil
}

func readDeliveryLease(path string) (Lease, error) {
	var lease Lease
	f, err := os.Open(path)
	if err != nil {
		return lease, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, 64*1024)).Decode(&lease)
	lease.Pending = nil
	lease.Task = model.TaskContext{}
	return lease, err
}

func publicLeaseStatus(status string) string {
	switch status {
	case "not_attached", "starting", "stopped", "unavailable":
		return status
	default:
		if status == "" {
			return ""
		}
		return "unavailable"
	}
}

func publicLeaseReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	base, suffix, cut := strings.Cut(reason, ":")
	switch base {
	case "lease_expired", "runtime_context_unavailable", "observer_stop_unconfirmed",
		"observer_start_failed", "observer_start_timeout", "observer_lost",
		"observer_cancelled_or_lost", "observer_failed", "pure_edit",
		"no_task_observation", "worktree_mismatch", "task_replaced",
		"invalid_observer_lease":
		if !cut {
			return base
		}
		// Keep a known qualifier, but never echo an arbitrary suffix that
		// might be an RPC error.
		switch strings.TrimSpace(suffix) {
		case "runtime_service_required", "project_not_registered_in_runtime", "timeout":
			return base + ":" + strings.TrimSpace(suffix)
		default:
			return base
		}
	default:
		return "unavailable"
	}
}
