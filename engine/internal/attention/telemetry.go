package attention

// This file contains the optional, local quality ledger for the agent-side
// attention adapter.  It is deliberately separate from runtime facts: records
// describe what the adapter observed and what the agent later reported, but
// they are never read by status, the daemon, or an execution command.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	TelemetrySchema       = "oberth.attention.telemetry/v1"
	FeedbackSchema        = "oberth.attention.feedback/v1"
	QualityDir            = "quality"
	QualityFile           = "events.ndjson"
	QualityBackupSuffix   = ".1"
	TelemetryMaxBytes     = 8 << 20
	TelemetryMaxLineBytes = 1 << 20
)

// TelemetryRecord is an append-only local observation.  It intentionally has
// no task text, log contents, API keys, shell commands, or absolute paths.
// Project and worktree identifiers are either a short label or a one-way hash.
type TelemetryRecord struct {
	Schema            string             `json:"schema"`
	RecordID          string             `json:"record_id"`
	RecordedAt        string             `json:"recorded_at"`
	Kind              string             `json:"kind"`   // assessment, feedback, lifecycle
	Source            string             `json:"source"` // runner, agent, adapter
	Project           string             `json:"project,omitempty"`
	WorktreeID        string             `json:"worktree_id,omitempty"`
	EventID           string             `json:"event_id,omitempty"`
	StateRevision     string             `json:"state_revision,omitempty"`
	ContextID         string             `json:"context_id,omitempty"`
	SessionID         string             `json:"session_id,omitempty"`
	EventKind         string             `json:"event_kind,omitempty"`
	Service           string             `json:"service,omitempty"`
	Status            string             `json:"status,omitempty"`
	Reason            string             `json:"reason,omitempty"`
	FirstSeen         string             `json:"first_seen,omitempty"`
	FreshUntil        string             `json:"fresh_until,omitempty"`
	LatencyMS         int64              `json:"latency_ms,omitempty"`
	Cached            bool               `json:"cached,omitempty"`
	Assessment        *Assessment        `json:"assessment,omitempty"`
	Decision          *TelemetryDecision `json:"decision,omitempty"`
	CandidateOptions  []string           `json:"candidate_options,omitempty"`
	AuthorizedOptions []string           `json:"authorized_options,omitempty"`
	Polls             int                `json:"polls,omitempty"`
	EventsSeen        int                `json:"events_seen,omitempty"`
	JevCalls          int                `json:"jev_calls,omitempty"`
	CacheHits         int                `json:"cache_hits,omitempty"`
	NoEventPolls      int                `json:"no_event_polls,omitempty"`
	Unavailable       int                `json:"unavailable,omitempty"`
	Feedback          *TelemetryFeedback `json:"feedback,omitempty"`
}

// TelemetryDecision is the adapter's actual conservative routing decision,
// which is distinct from Jev's needs_human probability.
type TelemetryDecision struct {
	NeedsHuman bool   `json:"needs_human"`
	Reason     string `json:"reason"`
	Policy     string `json:"policy,omitempty"`
}

// TelemetryFeedback is supplied by the development agent after it has shown a
// request to a person and, when applicable, run the chosen documented action.
// HumanNeeded is the optional ground-truth label used by quality reports.
type TelemetryFeedback struct {
	HumanNeeded  *bool  `json:"human_needed,omitempty"`
	TaskRelevant *bool  `json:"task_relevant,omitempty"`
	UserDecision string `json:"user_decision,omitempty"`
	ChosenOption string `json:"chosen_option,omitempty"`
	ActionStatus string `json:"action_status,omitempty"`
	Resolved     *bool  `json:"resolved,omitempty"`
}

// FeedbackInput is the small JSON document accepted by --feedback.  It is
// intentionally separate from TelemetryRecord so callers cannot inject a
// recorded timestamp, source, or arbitrary project path.
type FeedbackInput struct {
	Schema        string `json:"schema,omitempty"`
	EventID       string `json:"event_id"`
	StateRevision string `json:"state_revision"`
	ContextID     string `json:"context_id,omitempty"`
	WorktreeID    string `json:"worktree_id,omitempty"`
	Project       string `json:"project,omitempty"`
	HumanNeeded   *bool  `json:"human_needed,omitempty"`
	TaskRelevant  *bool  `json:"task_relevant,omitempty"`
	UserDecision  string `json:"user_decision,omitempty"`
	ChosenOption  string `json:"chosen_option,omitempty"`
	ActionStatus  string `json:"action_status,omitempty"`
	Resolved      *bool  `json:"resolved,omitempty"`
}

// QualityReport reports both raw observation volume and labelled quality.  A
// nil metric means that there are not enough labels to calculate it; callers
// must never interpret an absent metric as zero quality or perfect quality.
type QualityReport struct {
	Schema             string               `json:"schema"`
	GeneratedAt        string               `json:"generated_at"`
	Source             string               `json:"source"`
	WindowStart        string               `json:"window_start,omitempty"`
	ProjectFilter      string               `json:"project_filter,omitempty"`
	RecordsRead        int                  `json:"records_read"`
	AssessmentRecords  int                  `json:"assessment_records"`
	FeedbackRecords    int                  `json:"feedback_records"`
	UniqueCases        int                  `json:"unique_cases"`
	LabeledCases       int                  `json:"labeled_cases"`
	UnlabeledCases     int                  `json:"unlabeled_cases"`
	LabelCoverage      *float64             `json:"label_coverage"`
	TaskRelevantLabels int                  `json:"task_relevant_labels"`
	StatusCounts       map[string]int       `json:"status_counts"`
	EventCounts        map[string]int       `json:"event_counts"`
	ProjectCounts      map[string]int       `json:"project_counts"`
	DecisionReasons    map[string]int       `json:"decision_reason_counts"`
	RecommendedOptions map[string]int       `json:"recommended_option_counts"`
	ModelCounts        map[string]int       `json:"model_counts"`
	UserDecisions      map[string]int       `json:"user_decision_counts"`
	Availability       AvailabilityMetric   `json:"assessment_availability"`
	HumanDecision      ClassificationMetric `json:"human_decision"`
	ModelNeedsHuman    CalibrationMetric    `json:"model_needs_human"`
	ModelTaskRelevant  CalibrationMetric    `json:"model_task_relevant"`
	Recommendation     RateMetric           `json:"recommendation"`
	Actions            ActionMetric         `json:"actions"`
	Resolution         ResolutionMetric     `json:"resolution"`
	DetectionLatency   LatencyMetric        `json:"detection_latency"`
	Sessions           SessionMetric        `json:"sessions"`
}

// SessionMetric explains why a quality ledger has no Jev calls. The counts
// distinguish an idle/no-event session from an adapter that was never armed.
type SessionMetric struct {
	Started      int `json:"started"`
	Stopped      int `json:"stopped"`
	Errors       int `json:"errors"`
	Polls        int `json:"polls"`
	EventsSeen   int `json:"events_seen"`
	JevCalls     int `json:"jev_calls"`
	CacheHits    int `json:"cache_hits"`
	NoEventPolls int `json:"no_event_polls"`
	Unavailable  int `json:"unavailable"`
}

type AvailabilityMetric struct {
	Cases    int      `json:"cases"`
	Assessed int      `json:"assessed"`
	Missing  int      `json:"missing"`
	Rate     *float64 `json:"rate"`
}

type ClassificationMetric struct {
	TruePositive  int      `json:"true_positive"`
	TrueNegative  int      `json:"true_negative"`
	FalsePositive int      `json:"false_positive"`
	FalseNegative int      `json:"false_negative"`
	Precision     *float64 `json:"precision"`
	Recall        *float64 `json:"recall"`
	F1            *float64 `json:"f1"`
}

type CalibrationMetric struct {
	Samples           int      `json:"samples"`
	BrierScore        *float64 `json:"brier_score"`
	MeanAbsoluteError *float64 `json:"mean_absolute_error"`
}

type RateMetric struct {
	Samples int      `json:"samples"`
	Matches int      `json:"matches"`
	Rate    *float64 `json:"rate"`
}

type ActionMetric struct {
	Attempts    int      `json:"attempts"`
	Succeeded   int      `json:"succeeded"`
	Failed      int      `json:"failed"`
	SuccessRate *float64 `json:"success_rate"`
}

type ResolutionMetric struct {
	Samples    int      `json:"samples"`
	Resolved   int      `json:"resolved"`
	Unresolved int      `json:"unresolved"`
	Rate       *float64 `json:"rate"`
}

type LatencyMetric struct {
	Samples int     `json:"samples"`
	MinMS   int64   `json:"min_ms,omitempty"`
	MaxMS   int64   `json:"max_ms,omitempty"`
	MeanMS  float64 `json:"mean_ms,omitempty"`
}

// TelemetryPath returns the local quality ledger path.  BERTH_HOME is resolved
// by the caller so tests and isolated projects can keep their own ledger.
func TelemetryPath(home string) string {
	return filepath.Join(home, QualityDir, QualityFile)
}

func telemetryBackupPath(home string) string {
	return TelemetryPath(home) + QualityBackupSuffix
}

// WorktreeID is the stable, non-reversible identifier used in local telemetry
// and in the redacted Jev state.
func WorktreeID(root string) string { return hashWorktree(root) }

// ProjectLabel returns only the final directory component, with controls
// removed and a bounded length.  It is useful when reviewing local logs for
// several projects without persisting their absolute paths.
func ProjectLabel(root string) string {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" {
		return "unknown"
	}
	label := filepath.Base(root)
	label = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, label)
	if label == "" || label == "." || label == string(filepath.Separator) {
		return "unknown"
	}
	if len([]byte(label)) > 128 {
		label = string([]byte(label)[:128])
	}
	return label
}

// NewFeedbackRecord validates the agent's feedback and creates a local record.
// It does not require a live artifact because a user may report the result
// after the event has already recovered and its derived file has disappeared.
func NewFeedbackRecord(input FeedbackInput, now time.Time) (TelemetryRecord, error) {
	if strings.TrimSpace(input.EventID) == "" || strings.TrimSpace(input.StateRevision) == "" {
		return TelemetryRecord{}, errors.New("attention: feedback requires event_id and state_revision")
	}
	if input.Schema != "" && input.Schema != FeedbackSchema {
		return TelemetryRecord{}, fmt.Errorf("attention: unsupported feedback schema %q", input.Schema)
	}
	feedback := TelemetryFeedback{
		HumanNeeded: input.HumanNeeded, TaskRelevant: input.TaskRelevant, UserDecision: strings.TrimSpace(input.UserDecision),
		ChosenOption: strings.TrimSpace(input.ChosenOption), ActionStatus: strings.TrimSpace(input.ActionStatus),
		Resolved: input.Resolved,
	}
	if feedback.HumanNeeded == nil && feedback.TaskRelevant == nil && feedback.UserDecision == "" && feedback.ChosenOption == "" && feedback.ActionStatus == "" && feedback.Resolved == nil {
		return TelemetryRecord{}, errors.New("attention: feedback must contain a label, user decision, option, action status, or resolved value")
	}
	if !validUserDecision(feedback.UserDecision) {
		return TelemetryRecord{}, fmt.Errorf("attention: unsupported user_decision %q", feedback.UserDecision)
	}
	if feedback.ChosenOption != "" && !KnownOptionID(feedback.ChosenOption) {
		return TelemetryRecord{}, fmt.Errorf("attention: unsupported chosen_option %q", feedback.ChosenOption)
	}
	if !validActionStatus(feedback.ActionStatus) {
		return TelemetryRecord{}, fmt.Errorf("attention: unsupported action_status %q", feedback.ActionStatus)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	record := TelemetryRecord{
		Schema: TelemetrySchema, RecordID: telemetryID("feedback", input.EventID, input.StateRevision, input.ContextID, now),
		RecordedAt: now.UTC().Format(time.RFC3339Nano), Kind: "feedback", Source: "agent",
		Project: ProjectLabel(input.Project), WorktreeID: strings.TrimSpace(input.WorktreeID),
		EventID: strings.TrimSpace(input.EventID), StateRevision: strings.TrimSpace(input.StateRevision),
		ContextID: strings.TrimSpace(input.ContextID), Feedback: &feedback,
	}
	return normalizeTelemetry(record, now)
}

func validUserDecision(value string) bool {
	if value == "" {
		return true
	}
	switch value {
	case "selected", "continued", "dismissed", "no_response", "asked", "unknown":
		return true
	default:
		return false
	}
}

func validActionStatus(value string) bool {
	if value == "" {
		return true
	}
	switch value {
	case "succeeded", "failed", "not_run", "unknown":
		return true
	default:
		return false
	}
}

// AppendTelemetry appends one owner-readable record.  A malformed record or a
// ledger write failure is returned to the optional adapter; callers should keep
// the runtime/HIL path usable and surface the failure as a diagnostic.
func AppendTelemetry(home string, record TelemetryRecord) (string, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return "", errors.New("attention: empty telemetry home")
	}
	normalized, err := normalizeTelemetry(record, time.Now().UTC())
	if err != nil {
		return "", err
	}
	path := TelemetryPath(home)
	line, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("attention: encode telemetry: %w", err)
	}
	if len(line)+1 > TelemetryMaxLineBytes {
		return "", errors.New("attention: telemetry record is too large")
	}
	err = withArtifactLock(home, func() error {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if info, statErr := os.Stat(path); statErr == nil && info.Size()+int64(len(line)+1) > TelemetryMaxBytes {
			backup := telemetryBackupPath(home)
			_ = os.Remove(backup)
			if err := os.Rename(path, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			_ = os.Chmod(backup, 0o600)
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := file.Chmod(0o600); err != nil {
			return err
		}
		if _, err := file.Write(append(line, '\n')); err != nil {
			return err
		}
		return file.Sync()
	})
	if err != nil {
		return "", fmt.Errorf("attention: append telemetry: %w", err)
	}
	return path, nil
}

// ReadTelemetry reads the backup before the current ledger.  A malformed line
// is ignored so one interrupted/manual edit does not hide all later evidence.
func ReadTelemetry(home string) ([]TelemetryRecord, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil, errors.New("attention: empty telemetry home")
	}
	var records []TelemetryRecord
	for _, path := range []string{telemetryBackupPath(home), TelemetryPath(home)} {
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		// A rotated or manually damaged file can contain one oversized line;
		// allow the bounded ledger size to be skipped without hiding the valid
		// current file, while still keeping memory bounded.
		scanner.Buffer(make([]byte, 4096), TelemetryMaxBytes+1)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var record TelemetryRecord
			if err := json.Unmarshal([]byte(line), &record); err != nil || record.Schema != TelemetrySchema {
				continue
			}
			record, err = normalizeTelemetry(record, time.Time{})
			if err != nil {
				continue
			}
			records = append(records, record)
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].RecordedAt < records[j].RecordedAt })
	return records, nil
}

func normalizeTelemetry(record TelemetryRecord, now time.Time) (TelemetryRecord, error) {
	if record.Schema == "" {
		record.Schema = TelemetrySchema
	}
	if record.Schema != TelemetrySchema {
		return TelemetryRecord{}, fmt.Errorf("attention: unsupported telemetry schema %q", record.Schema)
	}
	if record.Kind != "assessment" && record.Kind != "feedback" && record.Kind != "lifecycle" {
		return TelemetryRecord{}, fmt.Errorf("attention: unsupported telemetry kind %q", record.Kind)
	}
	if record.Source == "" {
		record.Source = "adapter"
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if record.RecordedAt == "" {
		record.RecordedAt = now.UTC().Format(time.RFC3339Nano)
	} else if _, err := time.Parse(time.RFC3339Nano, record.RecordedAt); err != nil {
		return TelemetryRecord{}, errors.New("attention: invalid telemetry recorded_at")
	}
	if record.RecordID == "" {
		record.RecordID = telemetryID(record.Kind, record.EventID, record.StateRevision, record.ContextID, now)
	}
	for name, value := range map[string]string{
		"record_id": record.RecordID, "event_id": record.EventID, "state_revision": record.StateRevision,
		"context_id": record.ContextID, "session_id": record.SessionID, "worktree_id": record.WorktreeID, "event_kind": record.EventKind,
		"service": record.Service, "status": record.Status, "reason": record.Reason,
	} {
		if strings.ContainsAny(value, "\r\n") || len([]byte(value)) > 512 {
			return TelemetryRecord{}, fmt.Errorf("attention: invalid telemetry %s", name)
		}
	}
	for name, value := range map[string]int{
		"polls": record.Polls, "events_seen": record.EventsSeen, "jev_calls": record.JevCalls,
		"cache_hits": record.CacheHits, "no_event_polls": record.NoEventPolls, "unavailable": record.Unavailable,
	} {
		if value < 0 {
			return TelemetryRecord{}, fmt.Errorf("attention: invalid telemetry %s", name)
		}
	}
	record.Project = ProjectLabel(record.Project)
	record.CandidateOptions = cleanOptionIDs(record.CandidateOptions)
	record.AuthorizedOptions = cleanOptionIDs(record.AuthorizedOptions)
	if record.Assessment != nil {
		if err := validateTelemetryAssessment(*record.Assessment); err != nil {
			return TelemetryRecord{}, err
		}
		record.ContextID = firstNonEmpty(record.ContextID, record.Assessment.ContextID)
	}
	if record.Decision != nil {
		if err := validateTelemetryDecision(*record.Decision); err != nil {
			return TelemetryRecord{}, err
		}
	}
	if record.Feedback != nil {
		if !validUserDecision(record.Feedback.UserDecision) || !validActionStatus(record.Feedback.ActionStatus) {
			return TelemetryRecord{}, errors.New("attention: invalid telemetry feedback value")
		}
		if record.Feedback.ChosenOption != "" && !KnownOptionID(record.Feedback.ChosenOption) {
			return TelemetryRecord{}, fmt.Errorf("attention: unsupported chosen option %q", record.Feedback.ChosenOption)
		}
	}
	if len(record.EventID) == 0 && record.Kind != "lifecycle" && record.Status != "clear" && record.Status != "error" && record.Status != "stale" {
		return TelemetryRecord{}, errors.New("attention: non-clear telemetry requires event_id")
	}
	return record, nil
}

func validateTelemetryAssessment(assessment Assessment) error {
	if assessment.NextOption != "" && !KnownOptionID(assessment.NextOption) {
		return fmt.Errorf("attention: unsupported telemetry next option %q", assessment.NextOption)
	}
	for name, value := range map[string]string{
		"context_id": assessment.ContextID,
		"model":      assessment.Model,
	} {
		if strings.ContainsAny(value, "\r\n") || len([]byte(value)) > 256 || filepath.IsAbs(value) || strings.HasPrefix(value, "~") {
			return fmt.Errorf("attention: invalid telemetry assessment %s", name)
		}
	}
	for name, value := range map[string]*float64{"needs_human": assessment.NeedsHuman, "task_relevant": assessment.TaskRelevant, "confidence": assessment.Confidence} {
		if value != nil && (!finite(*value) || math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return fmt.Errorf("attention: invalid telemetry assessment %s", name)
		}
	}
	for option, probability := range assessment.Probabilities {
		if !KnownOptionID(option) || !finite(probability) {
			return fmt.Errorf("attention: invalid telemetry probability for %s", option)
		}
	}
	return nil
}

func validateTelemetryDecision(decision TelemetryDecision) error {
	for name, value := range map[string]string{"reason": decision.Reason, "policy": decision.Policy} {
		if strings.ContainsAny(value, "\r\n") || len([]byte(value)) > 256 {
			return fmt.Errorf("attention: invalid telemetry decision %s", name)
		}
	}
	return nil
}

func cleanOptionIDs(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && KnownOptionID(value) && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func telemetryID(kind, eventID, revision, context string, now time.Time) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", kind, eventID, revision, context, now.UnixNano())))
	return hex.EncodeToString(sum[:])[:24]
}

// BuildQualityReport computes metrics from the local ledger.  since is an
// absolute time; a zero value includes all retained records.  projectFilter
// matches either the safe project label or the stable worktree hash.
func BuildQualityReport(records []TelemetryRecord, since time.Time, projectFilter string, now time.Time) QualityReport {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	report := QualityReport{
		Schema: TelemetrySchema, GeneratedAt: now.UTC().Format(time.RFC3339Nano), Source: "local-quality-ledger",
		StatusCounts: map[string]int{}, EventCounts: map[string]int{}, ProjectCounts: map[string]int{},
		DecisionReasons: map[string]int{}, RecommendedOptions: map[string]int{}, ModelCounts: map[string]int{},
		UserDecisions: map[string]int{},
		ProjectFilter: strings.TrimSpace(projectFilter),
	}
	if !since.IsZero() {
		report.WindowStart = since.UTC().Format(time.RFC3339Nano)
	}
	latestAssessments := make(map[string]TelemetryRecord)
	feedbackByKey := make(map[string]TelemetryFeedback)
	feedbackTime := make(map[string]string)
	selected := make([]TelemetryRecord, 0, len(records))
	for _, record := range records {
		when, err := time.Parse(time.RFC3339Nano, record.RecordedAt)
		if err != nil || (!since.IsZero() && when.Before(since)) {
			continue
		}
		if projectFilter != "" && record.Project != projectFilter && record.WorktreeID != projectFilter {
			continue
		}
		report.RecordsRead++
		selected = append(selected, record)
		if record.Project != "" {
			report.ProjectCounts[record.Project]++
		}
		if record.Kind == "assessment" {
			report.AssessmentRecords++
			if record.Status != "" {
				report.StatusCounts[record.Status]++
			}
			if record.EventKind != "" {
				report.EventCounts[record.EventKind]++
			}
			if record.Decision != nil && record.Decision.Reason != "" {
				report.DecisionReasons[record.Decision.Reason]++
			} else if record.Reason != "" {
				report.DecisionReasons[record.Reason]++
			}
			if record.EventID != "" {
				key := telemetryCaseKey(record)
				if old, ok := latestAssessments[key]; !ok || old.RecordedAt < record.RecordedAt {
					latestAssessments[key] = record
				}
			}
		} else if record.Kind == "lifecycle" {
			switch record.Status {
			case "subscribed":
				report.Sessions.Started++
			case "stopped":
				report.Sessions.Stopped++
			case "error":
				report.Sessions.Errors++
			}
			report.Sessions.Polls += record.Polls
			report.Sessions.EventsSeen += record.EventsSeen
			report.Sessions.JevCalls += record.JevCalls
			report.Sessions.CacheHits += record.CacheHits
			report.Sessions.NoEventPolls += record.NoEventPolls
			report.Sessions.Unavailable += record.Unavailable
		}
	}
	// A feedback record may be written after an agent has acted, and rotated
	// ledgers can be read in an order different from the original append order.
	// Resolve context-less feedback against the newest assessment for its case
	// only after all assessments have been collected.
	baseToKey := make(map[string]string)
	for key, assessment := range latestAssessments {
		base := telemetryCaseBase(assessment)
		if oldKey, ok := baseToKey[base]; !ok || latestAssessments[oldKey].RecordedAt < assessment.RecordedAt {
			baseToKey[base] = key
		}
	}
	for _, record := range selected {
		if record.Kind == "feedback" {
			report.FeedbackRecords++
			key := telemetryCaseKey(record)
			if record.ContextID == "" {
				if resolved, ok := baseToKey[telemetryCaseBase(record)]; ok {
					key = resolved
				}
			}
			if oldAt, ok := feedbackTime[key]; !ok || oldAt < record.RecordedAt {
				if record.Feedback != nil {
					feedbackByKey[key] = mergeFeedback(feedbackByKey[key], *record.Feedback)
					feedbackTime[key] = record.RecordedAt
				}
			}
		}
	}
	report.UniqueCases = len(latestAssessments)
	for key, assessment := range latestAssessments {
		feedback, labeled := feedbackByKey[key]
		report.Availability.Cases++
		if assessment.Assessment != nil {
			report.Availability.Assessed++
			if assessment.Assessment.NextOption != "" {
				report.RecommendedOptions[assessment.Assessment.NextOption]++
			}
			if assessment.Assessment.Model != "" {
				report.ModelCounts[assessment.Assessment.Model]++
			}
		} else {
			report.Availability.Missing++
		}
		if labeled && feedback.UserDecision != "" {
			report.UserDecisions[feedback.UserDecision]++
		}
		predicted, hasPrediction := predictedHuman(assessment)
		if labeled && feedback.HumanNeeded != nil && hasPrediction {
			report.LabeledCases++
			if predicted && *feedback.HumanNeeded {
				report.HumanDecision.TruePositive++
			} else if !predicted && !*feedback.HumanNeeded {
				report.HumanDecision.TrueNegative++
			} else if predicted {
				report.HumanDecision.FalsePositive++
			} else {
				report.HumanDecision.FalseNegative++
			}
		}
		if labeled && feedback.HumanNeeded != nil && assessment.Assessment != nil && assessment.Assessment.NeedsHuman != nil {
			probability := *assessment.Assessment.NeedsHuman
			truthValue := 0.0
			if *feedback.HumanNeeded {
				truthValue = 1
			}
			errorValue := probability - truthValue
			report.ModelNeedsHuman.Samples++
			if report.ModelNeedsHuman.BrierScore == nil {
				zero := 0.0
				report.ModelNeedsHuman.BrierScore = &zero
			}
			if report.ModelNeedsHuman.MeanAbsoluteError == nil {
				zero := 0.0
				report.ModelNeedsHuman.MeanAbsoluteError = &zero
			}
			*report.ModelNeedsHuman.BrierScore += errorValue * errorValue
			*report.ModelNeedsHuman.MeanAbsoluteError += math.Abs(errorValue)
		}
		if labeled && feedback.TaskRelevant != nil && assessment.Assessment != nil && assessment.Assessment.TaskRelevant != nil {
			probability := *assessment.Assessment.TaskRelevant
			truthValue := 0.0
			if *feedback.TaskRelevant {
				truthValue = 1
			}
			errorValue := probability - truthValue
			report.TaskRelevantLabels++
			report.ModelTaskRelevant.Samples++
			if report.ModelTaskRelevant.BrierScore == nil {
				zero := 0.0
				report.ModelTaskRelevant.BrierScore = &zero
			}
			if report.ModelTaskRelevant.MeanAbsoluteError == nil {
				zero := 0.0
				report.ModelTaskRelevant.MeanAbsoluteError = &zero
			}
			*report.ModelTaskRelevant.BrierScore += errorValue * errorValue
			*report.ModelTaskRelevant.MeanAbsoluteError += math.Abs(errorValue)
		}
		if labeled && feedback.ChosenOption != "" && assessment.Assessment != nil && assessment.Assessment.NextOption != "" {
			report.Recommendation.Samples++
			if feedback.ChosenOption == assessment.Assessment.NextOption {
				report.Recommendation.Matches++
			}
		}
		if labeled {
			switch feedback.ActionStatus {
			case "succeeded", "failed":
				report.Actions.Attempts++
				if feedback.ActionStatus == "succeeded" {
					report.Actions.Succeeded++
				} else {
					report.Actions.Failed++
				}
			}
			if feedback.Resolved != nil {
				report.Resolution.Samples++
				if *feedback.Resolved {
					report.Resolution.Resolved++
				} else {
					report.Resolution.Unresolved++
				}
			}
		}
		if assessment.FirstSeen != "" {
			first, firstErr := time.Parse(time.RFC3339Nano, assessment.FirstSeen)
			observed, observedErr := time.Parse(time.RFC3339Nano, assessment.RecordedAt)
			if firstErr == nil && observedErr == nil && observed.After(first) {
				latency := observed.Sub(first).Milliseconds()
				if latency >= 0 {
					if report.DetectionLatency.Samples == 0 || latency < report.DetectionLatency.MinMS {
						report.DetectionLatency.MinMS = latency
					}
					if latency > report.DetectionLatency.MaxMS {
						report.DetectionLatency.MaxMS = latency
					}
					report.DetectionLatency.MeanMS += float64(latency)
					report.DetectionLatency.Samples++
				}
			}
		}
	}
	report.UnlabeledCases = report.UniqueCases - report.LabeledCases
	if report.UniqueCases > 0 {
		coverage := float64(report.LabeledCases) / float64(report.UniqueCases)
		report.LabelCoverage = &coverage
	}
	if report.DetectionLatency.Samples > 0 {
		report.DetectionLatency.MeanMS /= float64(report.DetectionLatency.Samples)
	}
	if report.ModelNeedsHuman.Samples > 0 {
		*report.ModelNeedsHuman.BrierScore /= float64(report.ModelNeedsHuman.Samples)
		*report.ModelNeedsHuman.MeanAbsoluteError /= float64(report.ModelNeedsHuman.Samples)
	}
	if report.ModelTaskRelevant.Samples > 0 {
		*report.ModelTaskRelevant.BrierScore /= float64(report.ModelTaskRelevant.Samples)
		*report.ModelTaskRelevant.MeanAbsoluteError /= float64(report.ModelTaskRelevant.Samples)
	}
	report.HumanDecision.Precision = ratio(report.HumanDecision.TruePositive, report.HumanDecision.TruePositive+report.HumanDecision.FalsePositive)
	report.HumanDecision.Recall = ratio(report.HumanDecision.TruePositive, report.HumanDecision.TruePositive+report.HumanDecision.FalseNegative)
	if report.HumanDecision.Precision != nil && report.HumanDecision.Recall != nil && *report.HumanDecision.Precision+*report.HumanDecision.Recall > 0 {
		value := 2 * (*report.HumanDecision.Precision * *report.HumanDecision.Recall) / (*report.HumanDecision.Precision + *report.HumanDecision.Recall)
		report.HumanDecision.F1 = &value
	}
	report.Recommendation.Rate = ratio(report.Recommendation.Matches, report.Recommendation.Samples)
	report.Actions.SuccessRate = ratio(report.Actions.Succeeded, report.Actions.Attempts)
	report.Availability.Rate = ratio(report.Availability.Assessed, report.Availability.Cases)
	report.Resolution.Rate = ratio(report.Resolution.Resolved, report.Resolution.Samples)
	return report
}

func predictedHuman(record TelemetryRecord) (bool, bool) {
	if record.Decision != nil {
		return record.Decision.NeedsHuman, true
	}
	switch record.Status {
	case "human_required":
		return true, true
	case "continue":
		return false, true
	default:
		return false, false
	}
}

func telemetryCaseBase(record TelemetryRecord) string {
	return strings.Join([]string{record.WorktreeID, record.Project, record.EventID, record.StateRevision}, "\x00")
}

func telemetryCaseKey(record TelemetryRecord) string {
	return telemetryCaseBase(record) + "\x00" + record.ContextID
}

func mergeFeedback(old, next TelemetryFeedback) TelemetryFeedback {
	if next.HumanNeeded != nil {
		old.HumanNeeded = next.HumanNeeded
	}
	if next.TaskRelevant != nil {
		old.TaskRelevant = next.TaskRelevant
	}
	if next.UserDecision != "" {
		old.UserDecision = next.UserDecision
	}
	if next.ChosenOption != "" {
		old.ChosenOption = next.ChosenOption
	}
	if next.ActionStatus != "" {
		old.ActionStatus = next.ActionStatus
	}
	if next.Resolved != nil {
		old.Resolved = next.Resolved
	}
	return old
}

func ratio(numerator, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

// ReadJSONValueStrict is shared by the CLI's feedback/report input paths.
func ReadJSONValueStrict(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, TelemetryMaxLineBytes))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("attention: JSON input must contain exactly one value")
	}
	return nil
}
