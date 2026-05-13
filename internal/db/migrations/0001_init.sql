CREATE TABLE IF NOT EXISTS application (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    icon       TEXT NOT NULL DEFAULT '',
    sort       INTEGER NOT NULL DEFAULT 0,
    deleted_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS service (
    id         TEXT PRIMARY KEY,
    app_id     TEXT NOT NULL,
    name       TEXT NOT NULL,
    sort       INTEGER NOT NULL DEFAULT 0,
    deleted_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS url (
    id               TEXT PRIMARY KEY,
    service_id       TEXT NOT NULL,
    name             TEXT NOT NULL,
    url              TEXT NOT NULL,
    check_enabled    INTEGER NOT NULL DEFAULT 1,
    interval_seconds INTEGER NOT NULL DEFAULT 60,
    sort             INTEGER NOT NULL DEFAULT 0,
    deleted_at       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS check_result (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    url_id      TEXT NOT NULL,
    ts          INTEGER NOT NULL,
    ok          INTEGER NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    latency_ms  INTEGER NOT NULL DEFAULT 0,
    error       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS check_result_url_ts ON check_result(url_id, ts DESC);

CREATE TABLE IF NOT EXISTS session (
    token      TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS session_expires ON session(expires_at);
