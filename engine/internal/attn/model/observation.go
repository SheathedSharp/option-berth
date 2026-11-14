package model

import (
	"fmt"
	"time"
)

// ActivityMetric describes bounded evidence of observation, not process liveness.
// Only a fresh per-poll lease establishes observing; subscribed alone never does.
type ActivityMetric struct {
	State             string `json:"state"`
	ObservingSessions int    `json:"observing_sessions"`
	LastObservedAt    string `json:"last_observed_at,omitempty"`
	LastStatus        string `json:"last_status,omitempty"`
	Reason            string `json:"reason,omitempty"`
	NoEvent           bool   `json:"no_event"`
}

type observationCounts struct {
	calls, hits, legacyCalls, legacyHits int
	perPoll, started                     bool
}

func summarizeObservations(report *QualityReport, records []TelemetryRecord, since, now time.Time) {
	report.Activity.State = "no_session"
	counts := make(map[string]*observationCounts)
	latest := make(map[string]TelemetryRecord)
	ended := make(map[string]time.Time)
	var recent TelemetryRecord
	var observed TelemetryRecord
	for _, r := range records {
		if r.Kind != "assessment" && r.Kind != "observation" && r.Kind != "lifecycle" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, r.RecordedAt)
		if err != nil || at.After(now) {
			continue
		}
		if recent.RecordedAt == "" || laterRecord(r, recent) {
			recent = r
		}
		c := counts[r.SessionID]
		if c == nil {
			c = &observationCounts{}
			counts[r.SessionID] = c
		}
		c.perPoll = c.perPoll || r.PerPoll
		if r.Kind != "lifecycle" {
			if observed.RecordedAt == "" || laterRecord(r, observed) {
				observed = r
			}
			c.calls += r.ModelCalls
			c.hits += r.CacheHits
			if r.Cached && r.CacheHits == 0 {
				c.hits++
			}
			if r.SessionID != "" && (latest[r.SessionID].RecordedAt == "" || laterRecord(r, latest[r.SessionID])) {
				latest[r.SessionID] = r
			}
		} else {
			if r.Status == "subscribed" {
				c.started = true
			}
			if r.Status == "stopped" || r.Status == "error" {
				if at.After(ended[r.SessionID]) {
					ended[r.SessionID] = at
				}
				c.legacyCalls = max(c.legacyCalls, r.ModelCalls)
				c.legacyHits = max(c.legacyHits, r.CacheHits)
			}
		}
	}
	for _, c := range counts {
		if c.legacyCalls > c.calls || c.legacyHits > c.hits {
			// Legacy cumulative snapshots cannot locate calls inside a partial window.
			// Preserve all-time/full-session compatibility; never invent their timing.
			if since.IsZero() || c.started {
				c.calls = max(c.calls, c.legacyCalls)
				c.hits = max(c.hits, c.legacyHits)
			} else if !c.perPoll {
				report.LegacyCountsUnallocated = true
			}
		}
		report.ModelCalls += c.calls
		report.CacheHits += c.hits
	}
	if observed.RecordedAt != "" {
		recent = observed
	}
	if recent.RecordedAt != "" {
		report.Activity.State = "recent"
		report.Activity.LastObservedAt = recent.RecordedAt
		report.Activity.LastStatus = activityStatus(recent.Status)
		if recent.Status == "error" || recent.Status == "unavailable" {
			report.Activity.State = "failed"
			report.Activity.Reason = activityReason(recent.Reason)
		}
	}
	var current TelemetryRecord
	for id, r := range latest {
		until, err := time.Parse(time.RFC3339Nano, r.ObservationUntil)
		at, _ := time.Parse(time.RFC3339Nano, r.RecordedAt)
		if err != nil || !r.PerPoll || !until.After(now) || until.Sub(at) > time.Minute || !ended[id].Before(at) {
			continue
		}
		report.Activity.ObservingSessions++
		if current.RecordedAt == "" || laterRecord(r, current) {
			current = r
		}
	}
	if report.Activity.ObservingSessions > 0 {
		report.Activity.State = "observing"
		report.Activity.LastObservedAt = current.RecordedAt
		report.Activity.LastStatus = activityStatus(current.Status)
		report.Activity.NoEvent = current.Status == "clear" && current.EventID == ""
		report.Activity.Reason = ""
		if current.Status == "error" || current.Status == "unavailable" {
			report.Activity.State = "failed"
			report.Activity.Reason = activityReason(current.Reason)
		}
	}
}

func laterRecord(a, b TelemetryRecord) bool {
	at, _ := time.Parse(time.RFC3339Nano, a.RecordedAt)
	bt, _ := time.Parse(time.RFC3339Nano, b.RecordedAt)
	return at.After(bt)
}

func activityReason(reason string) string {
	switch reason {
	case ReasonNoKey, ReasonDisabled, ReasonUnauthorized, ReasonRateLimited, ReasonTimeout, ReasonNetwork, ReasonProvider, ReasonBadResponse, ReasonInvalidConfig, ReasonStale, "runner_error", "status_unavailable", "invalid_task_context", "configuration_changed", "model_unavailable", "status_read_failed", "state_changed_refresh_required":
		return reason
	default:
		return "runner_error"
	}
}

// ActivitySummary never renders project, task, provider output, or a raw error.
func ActivitySummary(a ActivityMetric) string {
	switch a.State {
	case "observing":
		if a.NoEvent {
			return fmt.Sprintf("%d task session(s), recent poll: no eligible event", a.ObservingSessions)
		}
		return fmt.Sprintf("%d task session(s), recent poll: %s", a.ObservingSessions, activityStatus(a.LastStatus))
	case "failed":
		return "last observation failed (" + activityReason(a.Reason) + "); inspect task runner output"
	case "recent":
		return "past activity only; no fresh observation lease (not proof of a running session)"
	default:
		return "no task observation recorded; assessment permission alone does not start one"
	}
}

func activityStatus(status string) string {
	switch status {
	case "clear", "continue", "human_required", "unavailable", "error", "stale", "subscribed", "stopped", "assessed":
		return status
	default:
		return "unavailable"
	}
}
