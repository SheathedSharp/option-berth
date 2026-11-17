package model

// QualitySummary is the decision-sized view of the local ledger. `oberth attn
// status --json` is read at task entry, where the question is "is a runner
// observing, and is anything happening"; the calibration, routing and
// distribution tables answer a person's later question and stay behind the
// explicit --quality flag.
//
// Zero counts are kept in the document. A zero is a fact about the ledger — no
// label written yet, no model call recorded — not an absent field.
type QualitySummary struct {
	Activity          ActivityMetric `json:"activity"`
	RecordsRead       int            `json:"records_read"`
	AssessmentRecords int            `json:"assessment_records"`
	ModelCalls        int            `json:"model_calls"`
	CacheHits         int            `json:"cache_hits"`
	FeedbackRecords   int            `json:"feedback_records"`
	UniqueCases       int            `json:"unique_cases"`
	LabeledCases      int            `json:"labeled_cases"`
	UnlabeledCases    int            `json:"unlabeled_cases"`
	Sessions          SessionMetric  `json:"sessions"`
}

// Summary projects a report onto the fields a task-entry read acts on, without
// recomputing anything: the same ledger read answers both documents.
func (r QualityReport) Summary() QualitySummary {
	return QualitySummary{
		Activity:          r.Activity,
		RecordsRead:       r.RecordsRead,
		AssessmentRecords: r.AssessmentRecords,
		ModelCalls:        r.ModelCalls,
		CacheHits:         r.CacheHits,
		FeedbackRecords:   r.FeedbackRecords,
		UniqueCases:       r.UniqueCases,
		LabeledCases:      r.LabeledCases,
		UnlabeledCases:    r.UnlabeledCases,
		Sessions:          r.Sessions,
	}
}
