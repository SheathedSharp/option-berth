-- Workplaces: the durable record of work done in one worktree (migration 009).
--
-- A workplace is a named scope under a worktree. Its blocks are the work
-- items: a shell command the engine ran, or an agent task the engine bridged.
-- The table holds identity and the active pointer only; the blocks are the
-- record.
--
-- worktree is the canonical root path (symlinks resolved), so two spellings of
-- the same checkout are one set of workplaces. `active` marks the workplace
-- new work lands in; the partial unique index makes "at most one per worktree"
-- a property of the schema rather than a rule every writer has to remember.

CREATE TABLE IF NOT EXISTS workplaces (
    id         TEXT    PRIMARY KEY,
    worktree   TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    active     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_workplaces_worktree ON workplaces(worktree, created_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_workplaces_one_active
    ON workplaces(worktree) WHERE active = 1;

-- One row per recorded work item.
--
-- `source` says who observed it: `engine` for work this process started (the
-- run identity, pid and log path are then the engine's own), `reported` for
-- work a front-end watched itself and handed over. `output` is only ever
-- filled for reported blocks; engine blocks point at log_path instead, so the
-- log stays the one copy of a run's output.
CREATE TABLE IF NOT EXISTS workplace_blocks (
    id         TEXT    PRIMARY KEY,
    workplace  TEXT    NOT NULL REFERENCES workplaces(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    kind       TEXT    NOT NULL,
    source     TEXT    NOT NULL,
    input      TEXT    NOT NULL DEFAULT '',
    status     TEXT    NOT NULL,
    provider   TEXT    NOT NULL DEFAULT '',
    run_id     TEXT    NOT NULL DEFAULT '',
    pid        INTEGER NOT NULL DEFAULT 0,
    log_path   TEXT    NOT NULL DEFAULT '',
    output     TEXT    NOT NULL DEFAULT '',
    exit_code  INTEGER,
    started_at TEXT    NOT NULL,
    ended_at   TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_workplace_blocks_workplace ON workplace_blocks(workplace, seq);

CREATE UNIQUE INDEX IF NOT EXISTS idx_workplace_blocks_seq ON workplace_blocks(workplace, seq);
