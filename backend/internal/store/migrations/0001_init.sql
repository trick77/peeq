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
    ytdlp_version          TEXT NOT NULL DEFAULT ''
);

INSERT OR IGNORE INTO settings (id, format_preset, retention_days, throttle_base_seconds, min_free_gb, cookie_status)
VALUES (1, 'apple-1080p', 14, 10, 5, 'absent');

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
