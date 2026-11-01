package jev

// FeedbackRecordResult is where a recorded feedback document landed, so each
// command layer prints it in its own output shape.
type FeedbackRecordResult struct {
	Path     string
	RecordID string
}
