-- settings: single-user, one-row singleton (id is always 1).
CREATE TABLE settings (
    id                     INTEGER PRIMARY KEY CHECK (id = 1),
    cookie_text            TEXT NOT NULL DEFAULT '',
    cookie_status          TEXT NOT NULL DEFAULT 'absent' CHECK (cookie_status IN ('absent', 'valid', 'stale', 'blocked')),
    cookie_updated_at      TEXT,
    format_preset          TEXT NOT NULL DEFAULT 'apple-1080p',
    format_custom          TEXT NOT NULL DEFAULT '',
    limit_rate             TEXT NOT NULL DEFAULT '',
    throttle_base_seconds  INTEGER NOT NULL DEFAULT 10,
    retention_days         INTEGER NOT NULL DEFAULT 14,
    min_free_gb            INTEGER NOT NULL DEFAULT 5,
    min_video_duration_seconds INTEGER NOT NULL DEFAULT 180,
    ytdlp_version          TEXT NOT NULL DEFAULT ''
);

INSERT OR IGNORE INTO settings (id, format_preset, retention_days, throttle_base_seconds, min_free_gb, cookie_status, min_video_duration_seconds)
VALUES (1, 'apple-1080p', 14, 10, 5, 'absent', 180);

-- videos: one row per tracked YouTube video.
CREATE TABLE videos (
    id                       TEXT PRIMARY KEY,
    url                      TEXT NOT NULL,
    title                    TEXT NOT NULL DEFAULT '',
    channel_id               TEXT NOT NULL DEFAULT '',
    channel_name             TEXT NOT NULL DEFAULT '',
    duration_seconds         INTEGER,
    published_at             TEXT,
    description              TEXT NOT NULL DEFAULT '',
    thumbnail_path           TEXT NOT NULL DEFAULT '',
    media_path               TEXT NOT NULL DEFAULT '',
    filesize_bytes           INTEGER,
    format_used              TEXT NOT NULL DEFAULT '',
    requested_format         TEXT NOT NULL DEFAULT '',
    availability             TEXT NOT NULL DEFAULT 'unknown' CHECK (availability IN ('available', 'deleted', 'private', 'geo', 'unknown')),
    status                   TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'queued', 'downloading', 'downloaded', 'tombstoned', 'error')),
    error_message            TEXT NOT NULL DEFAULT '',
    sponsorblock_segments    TEXT NOT NULL DEFAULT '[]',
    watched                  INTEGER NOT NULL DEFAULT 0,
    watched_at               TEXT,
    resume_position_seconds  REAL NOT NULL DEFAULT 0,
    favorite                 INTEGER NOT NULL DEFAULT 0,
    favorited_at             TEXT,
    created_at               TEXT NOT NULL DEFAULT (datetime('now')),
    downloaded_at            TEXT
);

CREATE INDEX idx_videos_status ON videos(status);
CREATE INDEX idx_videos_channel_id ON videos(channel_id);

-- download_jobs: the download queue, one job per attempt-tracked download.
CREATE TABLE download_jobs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    video_id      TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    state         TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'running', 'done', 'failed', 'canceled')),
    priority      INTEGER NOT NULL DEFAULT 0,
    attempts      INTEGER NOT NULL DEFAULT 0,
    max_attempts  INTEGER NOT NULL DEFAULT 3,
    last_error    TEXT NOT NULL DEFAULT '',
    log_tail      TEXT NOT NULL DEFAULT '',
    enqueued_at   TEXT NOT NULL DEFAULT (datetime('now')),
    started_at    TEXT,
    finished_at   TEXT
);

CREATE INDEX idx_download_jobs_video_id ON download_jobs(video_id);
CREATE INDEX idx_download_jobs_state ON download_jobs(state);

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

-- channels: a tracked YouTube channel (identity only). Presence == "tracked".
CREATE TABLE channels (
    id          TEXT PRIMARY KEY,          -- the channel UCID (UC...)
    handle      TEXT NOT NULL DEFAULT '',  -- @handle if known
    name        TEXT NOT NULL DEFAULT '',
    avatar_path TEXT NOT NULL DEFAULT '',  -- reserved, unused in P2
    added_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

-- subscriptions: presence == "subscribed". One row per subscribed channel.
CREATE TABLE subscriptions (
    channel_id      TEXT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    autodownload    INTEGER NOT NULL DEFAULT 0,
    format_override TEXT NOT NULL DEFAULT '',
    baselined_at    TEXT,                       -- NULL until the first scan completes
    last_scanned_at TEXT,
    next_scan_at    TEXT NOT NULL,              -- set to now on subscribe
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_subscriptions_next_scan_at ON subscriptions(next_scan_at);

-- channel_videos: per-channel scan ledger + pending list + dedup set.
CREATE TABLE channel_videos (
    video_id         TEXT PRIMARY KEY,
    channel_id       TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    title            TEXT NOT NULL DEFAULT '',
    duration_seconds INTEGER,
    url              TEXT NOT NULL DEFAULT '',
    thumbnail_url    TEXT NOT NULL DEFAULT '',   -- REMOTE url (not a local path)
    state            TEXT NOT NULL DEFAULT 'seen' CHECK (state IN ('seen','pending','ignored','queued')),
    discovered_at    TEXT NOT NULL DEFAULT (datetime('now')),
    decided_at       TEXT
);
CREATE INDEX idx_channel_videos_channel ON channel_videos(channel_id);
CREATE INDEX idx_channel_videos_state ON channel_videos(state);
