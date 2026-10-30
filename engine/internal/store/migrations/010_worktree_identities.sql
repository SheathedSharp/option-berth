-- 010_worktree_identities.sql — every checkout identity the daemon has seen.
--
-- `${worktree}` is derived from a checkout's canonical root, so an identity is
-- only meaningful together with the root it was derived from. This table keeps
-- that pair after the directory is deleted or moved: the worktree's database and
-- data directory still carry the old token, and a reader needs to know which
-- checkouts those tokens belonged to.
--
-- This is an identity ledger, not runtime truth: it records where an identity
-- was observed, never whether its services ran. Existence is checked against the
-- filesystem when the ledger is read, so a row is not removed when its checkout
-- disappears.

CREATE TABLE IF NOT EXISTS worktree_identities (
    identity   TEXT PRIMARY KEY,
    root       TEXT NOT NULL,
    group_name  TEXT NOT NULL DEFAULT '',
    config_path TEXT NOT NULL DEFAULT '',
    project     TEXT NOT NULL DEFAULT '',
    worktree    TEXT NOT NULL DEFAULT '',
    first_seen TEXT NOT NULL,
    last_seen  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS worktree_identities_root
    ON worktree_identities(root);

CREATE INDEX IF NOT EXISTS worktree_identities_group
    ON worktree_identities(group_name);
