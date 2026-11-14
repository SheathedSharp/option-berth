package runtimesvc

import (
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// openStore opens the daemon's SQLite database and installs it on the runtime
// and the scanner. A database that cannot be opened is not fatal: live state is
// still served, only renames, pins and history go missing — strictly better
// than refusing to report ports over a disk problem.
//
// A database that was unusable and had to be recreated raises
// state.event{kind: "db_reset"}, queued so that the first subscriber sees it
// even though nobody is connected this early (daemon spec, "Error handling").
func (s *Server) openStore() {
	path := s.opts.DBPath
	if path == "" {
		path = store.Path()
	}
	db, err := store.Open(path)
	if err != nil {
		s.logger.Error("opening the database, continuing without renames, pins and history",
			"path", path, "error", err)
		return
	}
	s.setStore(db)
	s.logger.Info("database open", "path", db.DBPath())

	reset := db.Reset()
	if reset == nil {
		return
	}
	s.logger.Warn("database was unusable and has been recreated",
		"path", reset.Path, "moved_to", reset.MovedTo, "reason", reset.Reason)
	s.queueEvent(state.Event{
		Data: map[string]any{
			"recovered": true,
			"path":      reset.Path,
			"moved_to":  reset.MovedTo,
			"reason":    reset.Reason,
		},
		Kind: "db_reset",
		At:   time.Now().Format(time.RFC3339),
	})
}

// setStore installs an open store on the runtime and the scan loop. Tests hand
// a harness its temp database through it.
func (s *Server) setStore(db *store.Store) {
	s.runtime.Store = db
	s.runtime.DB = db.DB()
	s.loop.SetStore(db)
}

// closeStore releases the database when the daemon shuts down.
func (s *Server) closeStore() {
	db := s.runtime.Store
	if db == nil {
		return
	}
	if err := db.Close(); err != nil {
		s.logger.Warn("closing the database", "error", err)
	}
	s.runtime.Store, s.runtime.DB = nil, nil
}

// queueEvent holds an event until the next publish carries it out. Events can
// be raised before anyone is able to listen — db_reset happens while the socket
// is still coming up — and dropping them would hide the one condition a client
// most needs to know about.
func (s *Server) queueEvent(ev state.Event) {
	s.pendingMu.Lock()
	s.pending = append(s.pending, ev)
	s.pendingMu.Unlock()
}

// takePending drains the queued events for the next publish.
func (s *Server) takePending() []state.Event {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	pending := s.pending
	if len(pending) == 0 {
		return nil
	}
	s.pending = nil
	return pending
}
