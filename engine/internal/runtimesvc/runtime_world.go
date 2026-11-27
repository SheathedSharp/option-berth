package runtimesvc

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

func init() {
	RegisterHandler("runtime.world.read", handleRuntimeWorldRead)
}

// runWorldFact is the worktree and service a live run belongs to. It is the
// join an exit notification needs: RunExit carries the run id, not the
// worktree, and the snapshot may not have been re-read when the process dies.
type runWorldFact struct {
	worktree string
	service  string
	specHash string
	codeHead string
}

// RegisterWorldObserver installs a callback invoked with every newly reduced
// worktree world. Observers are called after the world map is unlocked and
// outside the subscription locks, so an observer may read the world or the
// server without deadlocking. A panicking observer is logged and ignored.
func (s *Server) RegisterWorldObserver(fn func(runtimecore.World)) {
	if fn == nil {
		return
	}
	s.worldMu.Lock()
	s.worldObservers = append(s.worldObservers, fn)
	s.worldMu.Unlock()
}

// World returns the last reduced world for a group name or worktree path. The
// path is matched after filepath.Clean, so a trailing slash is not a different
// worktree.
func (s *Server) World(group, worktreePath string) (runtimecore.World, bool) {
	s.worldMu.RLock()
	defer s.worldMu.RUnlock()
	if group != "" {
		if world, ok := s.worlds[group]; ok {
			return world, true
		}
	}
	if worktreePath != "" {
		if world, ok := s.worlds[filepath.Clean(worktreePath)]; ok {
			return world, true
		}
	}
	return runtimecore.World{}, false
}

func handleRuntimeWorldRead(_ context.Context, req *Request) (any, error) {
	var params rpc.RuntimeWorldReadParams
	if err := req.Bind(&params); err != nil {
		return nil, err
	}
	group := strings.TrimSpace(params.Group)
	worktreePath := strings.TrimSpace(params.WorktreePath)
	world, ok := req.HostContext.Server().World(group, worktreePath)
	if !ok {
		target := firstNonEmpty(group, worktreePath)
		if target == "" {
			target = "the requested worktree"
		}
		return nil, rpc.NewError(rpc.CodeNotFound,
			"no runtime world for "+target,
			"read `oberth status` in the worktree once, then retry")
	}
	return rpc.RuntimeWorldReadResult{World: world}, nil
}

// publishWorlds reduces one world per worktree after a scanner transition and
// publishes them to registered observers. It runs before the subscription
// fan-out takes subsMu, so no observer can deadlock on a subscriber list.
func (s *Server) publishWorlds(next state.Snapshot) {
	at := snapshotTime(next.At)
	nextExitErrors := s.resolveFailedExitErrors(next.Groups)
	var worlds []runtimecore.World
	facts := make(map[string]runWorldFact)
	for _, group := range next.Groups {
		if group.RootDir == nil || *group.RootDir == "" {
			continue
		}
		key := worktreeKey(group)
		s.recordGroupLoops(group, key, at, facts)
		exits, firstErrors := groupExitEvidence(group, nextExitErrors)
		world := runtimecore.ReduceWorld(runtimecore.WorldInput{
			Seq:         next.Seq,
			ObservedAt:  next.At,
			Group:       group,
			Ports:       next.Ports,
			Observation: next.Observation,
			Exits:       exits,
			FirstErrors: firstErrors,
			Loops:       s.loops.Signals(key, at),
		})
		worlds = append(worlds, world)
	}
	s.loops.Prune(at)

	s.worldMu.Lock()
	fresh := make(map[string]runtimecore.World, len(worlds)*2)
	for _, world := range worlds {
		fresh[world.Worktree.Group] = world
		if world.Worktree.Root != "" {
			fresh[filepath.Clean(world.Worktree.Root)] = world
		}
	}
	s.worlds = fresh
	s.runFacts = facts
	s.exitErrors = nextExitErrors
	observers := append([]func(runtimecore.World){}, s.worldObservers...)
	s.worldMu.Unlock()

	for _, world := range worlds {
		for _, observer := range observers {
			s.notifyWorldObserver(observer, world)
		}
	}
}

// resolveFailedExitErrors returns a bounded map of first-error lines keyed by
// failed LastExit run id. Healthy snapshots and unchanged failed runs perform
// no SQLite query and no log file read; resolved runs are dropped on the next
// publishWorlds swap.
func (s *Server) resolveFailedExitErrors(groups []state.Group) map[string]string {
	needed := map[string]string{} // runID -> fallback log path
	for _, group := range groups {
		if group.RootDir == nil || *group.RootDir == "" {
			continue
		}
		for _, svc := range group.Services {
			if svc.Running || svc.LastExit == nil || svc.LastExit.RunID == "" {
				continue
			}
			if !runtimecore.IsFailureExitReason(svc.LastExit.Reason) {
				continue
			}
			logPath := ""
			if svc.LogPath != nil {
				logPath = *svc.LogPath
			}
			needed[svc.LastExit.RunID] = logPath
		}
	}
	if len(needed) == 0 {
		return map[string]string{}
	}

	resolved := make(map[string]string, len(needed))
	missing := false
	s.worldMu.RLock()
	for runID := range needed {
		if text, ok := s.exitErrors[runID]; ok {
			resolved[runID] = text
		} else {
			missing = true
		}
	}
	s.worldMu.RUnlock()
	if !missing {
		return resolved
	}

	var rowsByID map[string]store.RunExitRow
	if s.host != nil && s.host.Store != nil {
		if rows, err := s.host.Store.RunExits(store.RunExitLimit); err == nil && len(rows) > 0 {
			rowsByID = make(map[string]store.RunExitRow, len(rows))
			for _, row := range rows {
				if row.ID != "" {
					rowsByID[row.ID] = row
				}
			}
		}
	}
	for runID, fallbackLog := range needed {
		if _, cached := resolved[runID]; cached {
			continue
		}
		if row, ok := rowsByID[runID]; ok {
			logPath := row.LogPath
			if logPath == "" {
				logPath = fallbackLog
			}
			resolved[runID] = runtimecore.FirstErrorFromExit(logPath, row.LogOffset, row.LastLines)
			continue
		}
		if fallbackLog != "" {
			resolved[runID] = runtimecore.FirstErrorInLogFile(fallbackLog, 0, runtimecore.DefaultFirstErrorLogBytes)
		} else if s.host == nil || s.host.Store == nil {
			resolved[runID] = ""
		}
	}
	return resolved
}

func groupExitEvidence(group state.Group, exitErrors map[string]string) ([]runtimecore.ExitEvidence, map[string]string) {
	var exits []runtimecore.ExitEvidence
	var firstErrors map[string]string
	for _, svc := range group.Services {
		if svc.Running || svc.LastExit == nil {
			continue
		}
		logPath := ""
		if svc.LogPath != nil {
			logPath = *svc.LogPath
		}
		firstErr := ""
		if svc.LastExit.RunID != "" {
			firstErr = exitErrors[svc.LastExit.RunID]
		}
		exits = append(exits, runtimecore.ExitEvidence{
			Service:    svc.Name,
			RunID:      svc.LastExit.RunID,
			Reason:     svc.LastExit.Reason,
			Code:       svc.LastExit.Code,
			At:         svc.LastExit.At,
			LogPath:    logPath,
			FirstError: firstErr,
		})
		if firstErr != "" {
			if firstErrors == nil {
				firstErrors = map[string]string{}
			}
			firstErrors[svc.Name] = firstErr
		}
	}
	return exits, firstErrors
}

// recordGroupLoops feeds the bounded window from the facts already on the
// snapshot. Recording is idempotent per run id, so re-publishing the same row
// is not a second crash.
func (s *Server) recordGroupLoops(group state.Group, key string, at time.Time, facts map[string]runWorldFact) {
	for _, svc := range group.Services {
		if svc.RunID != nil && *svc.RunID != "" && svc.Running {
			fact := runWorldFact{worktree: key, service: svc.Name}
			if svc.RuntimeSpecHash != nil {
				fact.specHash = *svc.RuntimeSpecHash
			}
			if svc.CodeHead != nil {
				fact.codeHead = *svc.CodeHead
			}
			facts[*svc.RunID] = fact
			started := at
			if svc.StartedAt != nil {
				started = parseSnapshotTime(*svc.StartedAt, at)
			}
			s.loops.RecordStart(key, svc.Name, *svc.RunID, fact.specHash, fact.codeHead, started)
		}
		if svc.Running || svc.LastExit == nil {
			continue
		}
		exited := at
		if svc.LastExit.At != "" {
			exited = parseSnapshotTime(svc.LastExit.At, at)
		}
		s.loops.RecordExit(key, svc.Name, svc.LastExit.RunID, svc.LastExit.Reason, exited)
	}
}

// recordRunExit is the OnRunExit half of the same feed. A run that ends between
// two scans contributes one exit; the snapshot path covers a run that was
// already ended before the service saw it.
func (s *Server) recordRunExit(exit RunExit) {
	if exit.RunID == "" {
		return
	}
	s.worldMu.RLock()
	fact, ok := s.runFacts[exit.RunID]
	s.worldMu.RUnlock()
	if !ok {
		return
	}
	s.loops.RecordExit(fact.worktree, fact.service, exit.RunID, exit.Reason, exit.ExitedAt)
}

func (s *Server) notifyWorldObserver(observer func(runtimecore.World), world runtimecore.World) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("runtime world observer panicked", "panic", recovered)
		}
	}()
	observer(world)
}

func worktreeKey(group state.Group) string {
	if group.RootDir != nil && *group.RootDir != "" {
		return filepath.Clean(*group.RootDir)
	}
	return group.Name
}

func snapshotTime(value string) time.Time {
	return parseSnapshotTime(value, time.Now().UTC())
}

func parseSnapshotTime(value string, fallback time.Time) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
