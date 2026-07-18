-- users: the app-local profile created from a verified Authentik OIDC
-- identity (or the dev auto-login identity). Vark is single-user; in
-- practice only one row is ever created, but the table stays keyed by
-- oidc_subject to support re-provisioning without changing shape later.
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    oidc_subject  TEXT NOT NULL UNIQUE,
    username      TEXT NOT NULL,
    email         TEXT NOT NULL DEFAULT '',
    display_name  TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen_at  TEXT
);

-- sessions: opaque browser sessions. Only the SHA-256 hash of the session
-- token is ever stored; the raw token lives solely in the vark_session cookie.
CREATE TABLE sessions (
    token_hash    TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at    TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_sessions_user_id ON sessions(user_id);
CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);
