package attention

// The adapter boundary between the deterministic artifact and the optional Jev
// client (internal/jev).
//
// internal/attention owns the artifact and its on-disk protocol: identity,
// atomic publication, the read-modify-write lock, and the staleness signal.
// internal/jev owns every call that leaves the machine. It needs the same lock
// when it appends its local quality ledger, because that ledger and the
// artifact share one owner directory; exposing the lock keeps the client from
// reaching back into a private helper and keeps the critical section identical
// to the one status uses.

// WithLock runs fn while holding this BERTH_HOME's attention lock. It is the
// same lock Write, CompareAndSwap and Clear take, so an adapter's
// read-modify-write cannot interleave with a status refresh in another process.
func WithLock(home string, fn func() error) error { return withArtifactLock(home, fn) }
