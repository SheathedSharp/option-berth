package scanner

import (
	"context"
	"time"
)

// ObservationBudget is the cooperative ceiling for a production observation,
// including admission, collection, attribution and pre-commit health. It does
// not detach or interrupt synchronous filesystem/store callbacks. Once a
// commit begins, its ordered publication still completes synchronously.
const ObservationBudget = 15 * time.Second

// observationContext bounds a stage by ObservationBudget and reuses an earlier
// parent deadline, avoiding a new timer for each nested stage.
func observationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return observationContextAt(time.Now, parent)
}

// observationContextAt is observationContext with its clock supplied. The
// reuse decision is whether the parent has at most ObservationBudget left, and
// a caller holding a deadline exactly that far away must keep its own deadline
// instead of paying for a second timer. Reading the clock through a parameter
// is what makes that boundary decidable in a test: time.Now advances between
// the two calls, so the exact case can never be constructed otherwise.
func observationContextAt(now func() time.Time, parent context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && deadline.Sub(now()) <= ObservationBudget {
		return parent, func() {}
	}
	return context.WithTimeout(parent, ObservationBudget)
}
