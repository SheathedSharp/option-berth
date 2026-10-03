-- 008_run_exits.sql — bounded history for runs that have ended.
--
-- Live runs remain in the daemon registry and the legacy runs.json mirror.
-- This table is the durable half: it lets runs.list and status explain a
-- service after the daemon that observed its exit has been restarted.

CREATE TABLE IF NOT EXISTS run_exits (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id       TEXT    NOT NULL,
    pid          INTEGER NOT NULL,
    group_name   TEXT    NOT NULL,
    service_name TEXT    NOT NULL,
    cmd          TEXT    NOT NULL DEFAULT '',
    cwd          TEXT    NOT NULL DEFAULT '',
    port_hint    INTEGER NOT NULL DEFAULT 0,
    started_at   TEXT    NOT NULL,
    config_path  TEXT    NOT NULL DEFAULT '',
    start_id     TEXT    NOT NULL DEFAULT '',
    origin       TEXT    NOT NULL DEFAULT '',
    log_path     TEXT    NOT NULL DEFAULT '',
    log_offset   INTEGER NOT NULL DEFAULT 0,
    code         INTEGER NOT NULL,
    reason       TEXT    NOT NULL,
    exited_at    TEXT    NOT NULL,
    last_lines   TEXT    NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS run_exits_service_time
    ON run_exits(group_name, service_name, exited_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS run_exits_time
    ON run_exits(exited_at DESC, id DESC);
