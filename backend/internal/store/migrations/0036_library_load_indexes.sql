-- Indexes for what the Library, Inbox and channel list read on every load.
--
-- Shape only: no row changes.
--
-- 1. Image version stamps. video_thumbnails, pending_thumbnails and
--    channel_images each declare `bytes BLOB` AHEAD of `updated_at`. Every
--    list reads updated_at per row (it is the ?v= in the image URL), and a
--    column stored behind a blob can only be reached by walking that blob's
--    overflow chain: a 100 KB poster is two dozen page reads to learn one
--    timestamp, once per listed video. The primary-key autoindex holds the key
--    alone, so it does not help. These carry the stamp, which makes the read
--    index-only; the image itself is touched only when it is served.
--    The readers name these with INDEXED BY: left to itself the planner takes
--    the unique autoindex (one row guaranteed beats covering) and goes back to
--    the table for the stamp.
--
-- 2. The Library's chip counts (videos.Store.Counts). They filter on status,
--    watched, favorite and resume_position_seconds and group by category, all
--    of which sit behind description/summary/chapters/key_points in the row.
--    idx_videos_counts covers the statement and leads with category so the
--    GROUP BY needs no sort. idx_videos_category is its prefix and goes.
--
-- 3. The Library grid (videos.Store.List). It shows a card's worth of each
--    video, and nearly all of those columns sit behind the description and the
--    analysis text too. idx_videos_cards holds every column the list selects,
--    filters or sorts on, so the grid is read from the index and no video row
--    is opened until one is played. Wide for an index, small next to the rows
--    it stands in for: about a quarter of a kilobyte per video.
--
-- 4. Per-channel numbers. "Pending for this channel" could only use
--    idx_channel_videos_state, so it walked every pending row in the library
--    per channel; "newest ledger row per channel" read every ledger row;
--    "downloaded for this channel" filtered status after the seek. Each new
--    index leads with channel_id, so the single-column indexes they replace
--    are dropped rather than left for the planner to choose between: peeq
--    never runs ANALYZE, and with no statistics it picks by column count.
CREATE INDEX idx_video_thumbnails_version ON video_thumbnails (video_id, updated_at);
CREATE INDEX idx_pending_thumbnails_version ON pending_thumbnails (video_id, updated_at);
CREATE INDEX idx_channel_images_version ON channel_images (channel_id, kind, updated_at);

CREATE INDEX idx_videos_counts
    ON videos (category, status, watched, favorite, resume_position_seconds);
DROP INDEX IF EXISTS idx_videos_category;

CREATE INDEX idx_videos_cards
    ON videos (status, id, title, channel_id, channel_name, duration_seconds,
               published_at, media_path, watched, watched_at,
               resume_position_seconds, favorite, downloaded_at, category,
               created_at);

CREATE INDEX idx_channel_videos_channel_state ON channel_videos (channel_id, state);
CREATE INDEX idx_channel_videos_channel_discovered ON channel_videos (channel_id, discovered_at);
DROP INDEX IF EXISTS idx_channel_videos_channel;

CREATE INDEX idx_videos_channel_status ON videos (channel_id, status);
DROP INDEX IF EXISTS idx_videos_channel_id;
