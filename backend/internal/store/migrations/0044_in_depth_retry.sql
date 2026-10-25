-- The in-depth summary's retry queue. The step is best-effort inside a summary
-- job (key points and the index must not wait on it), so a failed call used to
-- leave the video without one for good: nothing ever asked again. A failure
-- now leaves a row here, and the summary worker's idle turn retries it until
-- it succeeds.
--
-- A table, not a flag on videos: the idle poll runs every couple of seconds and
-- must stay off the videos table, whose small columns sit behind its big ones
-- on a cold disk. This one holds a handful of rows at most.
--
-- last_error keeps the cause in the database: the log line that held it is gone
-- once the container is recreated.
CREATE TABLE in_depth_retry (
    video_id   TEXT PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE,
    attempts   INTEGER NOT NULL DEFAULT 0,
    next_at    TEXT NOT NULL DEFAULT (datetime('now')),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_in_depth_retry_next ON in_depth_retry (next_at);
