-- 0022: move the video poster out of the filesystem and into the database.
--
-- Until now a poster was a FILE plus a videos.thumbnail_path POINTER at it, and
-- the two could drift: Upsert wrote thumbnail_path unguarded, so every metadata
-- write that had no poster to offer (a channel scan, the inbox caption fetcher,
-- add-by-URL) blanked the column while the file sat untouched on disk. The card
-- then fell back to its gradient placeholder forever. For a tombstoned video
-- that is permanent: it never downloads again, so nothing ever repopulates the
-- pointer.
--
-- A poster is tens of kilobytes, written once and never edited — small enough
-- that storing it beside its row is worth more than storing it in the media
-- tree. has_thumbnail then means "the bytes are right here" rather than "a
-- column claims a file exists", and no write to any path column can make it lie.
-- The media file, the subtitles and the info.json stay on disk: those are
-- megabytes, and the download path's atomic .staging/ -> rename model is built
-- around them.
--
-- A side table rather than a column on videos: videoColumns selects the whole
-- row for every list and search read, and a BLOB there would drag every poster
-- through queries that only want titles.
--
-- No data moves here — SQL cannot read a file. The existing posters are carried
-- in by the thumbimport worker, which also recovers the rows whose pointer was
-- blanked while their file survived. videos.thumbnail_path is deliberately KEPT:
-- it is that worker's map of where to look.
CREATE TABLE video_thumbnails (
    video_id    TEXT PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE,
    mime        TEXT NOT NULL,
    bytes       BLOB NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
