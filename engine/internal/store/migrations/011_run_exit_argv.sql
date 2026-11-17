-- The exact argument vector a finished run was started with. Old rows read as
-- NULL: their display command survives in cmd, but they cannot be restarted
-- faithfully, and a restart must not guess where the quotes were.
ALTER TABLE run_exits ADD COLUMN argv TEXT;
