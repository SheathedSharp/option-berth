package rpc

// The workplace.* methods: a worktree's recorded work, as the daemon serves
// it. A workplace is a named scope under one worktree; its blocks are the
// shell commands and agent tasks recorded in it.
//
// These are the wire shapes only. What a block means, which statuses exist and
// how a selector resolves are internal/workplace's business.

// Workplace is one workplace as a client reads it. Blocks is how many blocks
// it holds; the blocks themselves come from workplace.show.
type Workplace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Worktree  string `json:"worktree"`
	Active    bool   `json:"active"`
	Blocks    int    `json:"blocks"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// WorkplaceBlock is one recorded work item.
type WorkplaceBlock struct {
	ID        string `json:"id"`
	Workplace string `json:"workplace"`
	Seq       int64  `json:"seq"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Input     string `json:"input,omitempty"`
	Status    string `json:"status"`
	Provider  string `json:"provider,omitempty"`
	// RunID is the engine run a shell block recorded. It is the same id
	// `runs.list` reports, which is what makes a block checkable against the
	// run it claims.
	RunID string `json:"run_id,omitempty"`
	PID   int    `json:"pid,omitempty"`
	// LogPath is where the run's output is. Engine blocks point at the log
	// rather than copying it, so the log stays the one copy.
	LogPath string `json:"log_path,omitempty"`
	// Output is only filled for reported blocks — work the engine did not
	// observe and therefore cannot point at a log for.
	Output   string `json:"output,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	// StartedAt is RFC 3339 UTC. EndedAt is absent while the work is running.
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
}

// WorkplaceBlockInput is what a reporter hands over. Everything but kind is
// optional: a report of work that just started says so and nothing more.
type WorkplaceBlockInput struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind"`
	Source    string `json:"source,omitempty"`
	Input     string `json:"input,omitempty"`
	Status    string `json:"status,omitempty"`
	Provider  string `json:"provider,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	PID       int    `json:"pid,omitempty"`
	LogPath   string `json:"log_path,omitempty"`
	Output    string `json:"output,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
}

// WorkplaceParams is the common half of every workplace method: which worktree
// the call is about.
type WorkplaceParams struct {
	HostParams
	Worktree string `json:"worktree"`
}

type WorkplaceListResult struct {
	Worktree string `json:"worktree"`
	// Active is the id of the workplace new work lands in; empty when the
	// worktree has none yet.
	Active     string      `json:"active,omitempty"`
	Workplaces []Workplace `json:"workplaces"`
}

type WorkplaceNewParams struct {
	HostParams
	Worktree string `json:"worktree"`
	// Name is what to call it. Empty takes the default name.
	Name string `json:"name,omitempty"`
}

type WorkplaceNewResult struct {
	Workplace Workplace `json:"workplace"`
}

type WorkplaceUseParams struct {
	HostParams
	Worktree string `json:"worktree"`
	Selector string `json:"selector"`
}

type WorkplaceUseResult struct {
	Workplace Workplace `json:"workplace"`
}

type WorkplaceShowParams struct {
	HostParams
	Worktree string `json:"worktree"`
	// Selector names the workplace; empty means the active one.
	Selector string `json:"selector,omitempty"`
	// Limit caps how many of the newest blocks come back; zero means all of
	// them (up to the workplace's own cap).
	Limit int `json:"limit,omitempty"`
}

type WorkplaceShowResult struct {
	Workplace Workplace        `json:"workplace"`
	Blocks    []WorkplaceBlock `json:"blocks"`
	// Truncated reports that the workplace holds more blocks than this
	// response carries, so a reader does not mistake a capped list for the
	// whole record.
	Truncated bool `json:"truncated"`
}

type WorkplaceDeleteParams struct {
	HostParams
	Worktree string `json:"worktree"`
	Selector string `json:"selector"`
}

type WorkplaceDeleteResult struct {
	Deleted string `json:"deleted"`
	// Active is the workplace left active afterwards, absent when the worktree
	// has none left. It is the whole row rather than an id so a caller can say
	// where new work will land without a second call.
	Active *Workplace `json:"active,omitempty"`
}

type WorkplaceRecordParams struct {
	HostParams
	Worktree string              `json:"worktree"`
	Block    WorkplaceBlockInput `json:"block"`
}

type WorkplaceRecordResult struct {
	Workplace Workplace      `json:"workplace"`
	Block     WorkplaceBlock `json:"block"`
}
