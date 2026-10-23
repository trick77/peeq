-- 0024: drop the four columns that pointed at files.
--
-- 0022 and 0023 moved every asset except the video itself into the database and
-- kept these columns deliberately, as the diskimport worker's map of where the
-- old files lived. That worker has now run in production — it logged
-- "sweep complete" on 2026-07-31, having carried the library in and tidied what
-- earlier bugs had left behind — and this migration ships with its deletion.
--
-- Nothing reads a path any more. A poster, a transcript and a channel's artwork
-- are bytes on a row; whether one exists is an EXISTS over the table that holds
-- it, not a string being non-empty. That is the whole point of the move: a
-- column claiming a file exists was something any write could get wrong, and
-- did.
--
-- 0013 still runs "UPDATE videos SET thumbnail_path = ''" and keeps working: on
-- a fresh database the files replay in order, so it runs long before this one
-- takes the column away.
ALTER TABLE videos DROP COLUMN thumbnail_path;
ALTER TABLE videos DROP COLUMN subtitle_path;
ALTER TABLE channels DROP COLUMN avatar_path;
ALTER TABLE channels DROP COLUMN banner_path;

-- Why a plain DROP COLUMN and NOT the table rebuild 0014 does.
--
-- DROP COLUMN needs SQLite 3.35+; the WASM binary this build links (from
-- sqlite-vec-go-bindings, not ncruces/go-sqlite3/embed) is 3.47.2. None of the
-- four columns is a primary key, unique, indexed, named in a CHECK or in a
-- partial index's WHERE, or referenced by a trigger, view, generated column or
-- foreign key — so nothing blocks the statement.
--
-- The rebuild would be far worse than unnecessary here. 0014 spells out its own
-- precondition: nothing in the schema has a foreign key TO channel_videos, so
-- its DROP/RENAME is safe with foreign_keys ON. That precondition is FALSE for
-- videos and channels — download_jobs, transcript_chunks, share_links,
-- playback_state, video_thumbnails, video_transcripts, subscriptions,
-- channel_videos and channel_images all reference them, almost all with
-- ON DELETE CASCADE. DROP TABLE with foreign_keys ON performs an implicit
-- DELETE FROM and fires every one of those cascades, and the runner's
-- connection is opened with foreign_keys(on). Nor can a migration turn them
-- off: PRAGMA foreign_keys is a no-op inside a transaction, and store.Migrate
-- wraps every file in one. A rebuild of either table would delete the library.
--
-- (0014's comment is now stale on its own terms, too: 0023 added
-- pending_thumbnails.video_id REFERENCES channel_videos(video_id), so a future
-- channel_videos rebuild carries the same hazard. Noted here rather than by
-- editing a migration that has already been applied everywhere.)
