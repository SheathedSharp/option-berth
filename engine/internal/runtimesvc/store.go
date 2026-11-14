package runtimesvc

import (
	"fmt"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// openStore installs only the Store claimed before listen. A missing owner
// never selects a storeless host or opens another database connection.
func (s *Server) openStore() error {
	if s.storage == nil || s.storage.database == nil {
		return fmt.Errorf("Runtime Service database ownership was not acquired")
	}
	db := s.storage.database
	s.setStore(db)
	s.logger.Info("database open", "path", db.DBPath())
	return nil
}

// setStore installs an open store on the runtime and the scan loop. Tests hand
// a harness its temp database through it.
func (s *Server) setStore(db *store.Store) {
	s.host.Store = db
	s.host.DB = db.DB()
	s.loop.SetStore(db)
}

// closeStore releases the database when the runtime service shuts down.
func (s *Server) closeStore() {
	db := s.host.Store
	if db == nil {
		return
	}
	if err := db.Close(); err != nil {
		s.logger.Warn("closing the database", "error", err)
	}
	if s.storage != nil {
		s.storage.database = nil
	}
	s.host.Store, s.host.DB = nil, nil
}
