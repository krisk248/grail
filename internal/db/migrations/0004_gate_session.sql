-- Viewer gate sessions. Kept in a SEPARATE table from `session` so a viewer's
-- gate cookie can never satisfy admin authentication.
CREATE TABLE IF NOT EXISTS gate_session (
    token      TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS gate_session_expires ON gate_session(expires_at);
