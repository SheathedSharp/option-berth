-- Every statement is idempotent: `prepare:` reruns on every up and restart.
CREATE TABLE IF NOT EXISTS schema_migrations (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
INSERT OR IGNORE INTO schema_migrations (id, name) VALUES (1, '0001_notes');

CREATE TABLE IF NOT EXISTS notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    body TEXT NOT NULL
);
