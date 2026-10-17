-- Per-channel category histograms (peeq's category and YouTube's own
-- yt_categories), read by the MCP server on every discovery and tool listing.
--
-- Both columns sit behind the description and the analysis text, so reading
-- them from the table walks overflow pages per video (see 0032). This index
-- carries everything the statement reads, keeping it index-only.
CREATE INDEX idx_videos_channel_categories ON videos (channel_id, category, yt_categories);
