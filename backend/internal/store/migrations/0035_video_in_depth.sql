-- The in-depth summary: a second, longer reading of the video built around its
-- key points, written by the summarize worker after the short summary.
--
-- Its own table rather than a column on videos. It runs to several kilobytes,
-- and every whole-row read of videos (Get, the classify sweep, the watched
-- sweep) would otherwise drag it through overflow pages for nothing; only the
-- video page reads it. Same reasoning as video_transcripts (0023).
--
-- No backfill: existing videos have no row until they are re-analysed.
CREATE TABLE video_in_depth (
    video_id   TEXT PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE,
    body       TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
