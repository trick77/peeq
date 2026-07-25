-- yt-dlp hands peeq the complete info dict on every metadata call (-J) and
-- every download (--write-info-json); until now all but nine fields were
-- discarded at unmarshal time. These four are the ones worth keeping.
--
-- yt_tags / yt_categories are prefixed on purpose. videos.category already
-- exists and means peeq's OWN classification (the fixed enum in
-- internal/videos/category.go); a bare `categories` column sitting next to it
-- would read as the same thing. These are YouTube's labels, not peeq's.
--
-- Both list columns follow the JSON-in-TEXT pattern already used by chapters,
-- key_points and sponsorblock_segments: '[]', never NULL.
ALTER TABLE videos ADD COLUMN media_type    TEXT NOT NULL DEFAULT '';
ALTER TABLE videos ADD COLUMN live_status   TEXT NOT NULL DEFAULT '';
ALTER TABLE videos ADD COLUMN yt_tags       TEXT NOT NULL DEFAULT '[]';
ALTER TABLE videos ADD COLUMN yt_categories TEXT NOT NULL DEFAULT '[]';
