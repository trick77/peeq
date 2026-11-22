-- idx_videos_cards gains summary_status, so list_videos (videos.Store.Browse)
-- reads it from the index like every other card column: the column sits
-- behind description/summary/chapters/key_points in the row, and reading it
-- there walks each video's overflow pages.
--
-- Shape only: no row changes. 0036's columns stay in 0036's order and the new
-- one goes last, so every INDEXED BY idx_videos_cards reader keeps its plan.
-- The rebuild reads every video row once, at this boot only.
DROP INDEX IF EXISTS idx_videos_cards;
CREATE INDEX idx_videos_cards
    ON videos (status, id, title, channel_id, channel_name, duration_seconds,
               published_at, media_path, watched, watched_at,
               resume_position_seconds, favorite, downloaded_at, category,
               created_at, summary_status);
