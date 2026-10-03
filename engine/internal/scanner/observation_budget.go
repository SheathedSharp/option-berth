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

// Reuse an earlier parent deadline, avoiding a new timer for each nested stage.
func observationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= ObservationBudget {
		return parent, func() {}
	}
	return context.WithTimeout(parent, ObservationBudget)
}
