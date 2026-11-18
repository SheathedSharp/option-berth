package ports

import (
	"errors"
	"fmt"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports/breaker"
)

// attemptPrimary runs the platform's primary collector unless its breaker holds
// it back, and reports what the attempt produced.
//
// The returned skip is non-nil exactly when the primary did not run, and the
// error that comes with it is the primary's verdict for this read: the failure
// that opened the breaker, plus the fact that this round did not retry it.
//
// A chain with a single rung is never held back. There is no alternative to read
// instead, so skipping the only collector would replace one classified failure
// with a scan that can produce nothing at all.
func attemptPrimary(rungs []Collector, run listenerCommand, gate *breaker.Breaker) ([]ListeningPort, error, *breaker.Skip) {
	primary := rungs[0]
	if gate != nil && len(rungs) > 1 {
		attempt, held := gate.Admit()
		if !attempt {
			held.Collector = primary.Name
			return nil, skipFailure(primary.Name, held), &held
		}
		rows, err := primary.collect(run)
		recordPrimary(gate, rows, err)
		return rows, err, nil
	}
	rows, err := primary.collect(run)
	return rows, err, nil
}

// recordPrimary tells the breaker what the attempt it admitted produced.
//
// The three outcomes are the chain's own classification, not a second one: a
// failure is exactly the pass retryable sends to the next rung, a withdrawal is
// the caller leaving, and everything else — a clean table, readable rows from a
// partial pass, a decoder's verdict — is the primary answering for itself.
func recordPrimary(gate *breaker.Breaker, rows []ListeningPort, err error) {
	switch {
	case err == nil:
		gate.Succeeded()
	case withdrawn(err):
		gate.Abandoned()
	case retryable(rows, err):
		gate.Failed(err)
	default:
		gate.Succeeded()
	}
}

// skipClause is the sentence a published verdict carries when a breaker held the
// platform's primary collector back. It is joined to the coverage sentence, not
// substituted for it: the table is narrower because of the collector that
// answered, and it is missing the primary's own rows besides.
func skipClause(skip breaker.Skip) string {
	return fmt.Sprintf("the primary collector %s was not run: %s", skip.Collector, skipReason(skip))
}

// skipReason is the evidence behind a skip, in the terms a reader needs: how
// many failures in a row, since when, and how long until it is tried again.
func skipReason(skip breaker.Skip) string {
	return fmt.Sprintf("%d failures in a row since %s, retried every %s",
		skip.Failures, skip.Since.UTC().Format(time.RFC3339), skip.Cooldown)
}

// skipFailure is the primary collector's verdict when the breaker did not run it
// and no alternative answered either.
//
// It keeps the last real failure's code and hint, so a sandbox that denied the
// table is still classified as that rather than as a generic error, and says in
// the same sentence that this round did not retry it. A last failure that never
// carried a code — a tool that is not installed — stays a plain error for the
// same reason it is one elsewhere.
func skipFailure(name string, skip breaker.Skip) error {
	detail := name + ": not run: " + skipReason(skip)
	var observed *ObservationError
	switch {
	case skip.Last == nil:
		return &ObservationError{
			Code: CodeObservationIncomplete, Detail: detail, Hint: observationIncompleteHint,
		}
	case errors.As(skip.Last, &observed):
		return &ObservationError{
			Code:   observed.Code,
			Detail: detail + "; last failure: " + firstObservationLine(observed.Error()),
			Hint:   observed.Hint,
			Err:    observed,
		}
	default:
		return fmt.Errorf("%s; last failure: %w", detail, skip.Last)
	}
}
