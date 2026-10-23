-- 0012: retire the word "tracked" from the channels table, and make channels
-- you only have downloaded videos from visible in the Channels list.
--
-- The UI stopped saying "tracked" some time ago — a channel is "added" (the
-- channel page says "Add this channel" / "Not added"), and separately it is
-- "subscribed". The schema kept the old word, so the two vocabularies had
-- drifted apart. Renaming makes them agree:
--
--   added_at   -> first_seen_at : when peeq first created the row at all. A
--                                 row exists for any channel peeq has looked
--                                 at, including ones the user never added.
--   tracked_at -> added_at      : when the user explicitly added the channel.
--
-- The invariant carried over from 0001_init.sql still holds, only reworded:
-- being added is NOT "a row exists", it is added_at IS NOT NULL. The scan
-- scheduler and the subscribe/delete guards all depend on that distinction.
--
-- Order matters: added_at has to be vacated before tracked_at can take the
-- name. SQLite rewrites the index definition on RENAME COLUMN, so the index
-- would survive under its old name — it is dropped and recreated so the name
-- matches the column again.
ALTER TABLE channels RENAME COLUMN added_at   TO first_seen_at;
ALTER TABLE channels RENAME COLUMN tracked_at TO added_at;

DROP INDEX IF EXISTS idx_channels_tracked_at;
CREATE INDEX idx_channels_added_at ON channels(added_at);

-- Backfill a cache-only row for every channel the library already holds a
-- downloaded video from. Adding a single video by URL never created one (the
-- download path writes videos.channel_id, and videos has no FK to channels),
-- so those channels were invisible in the Channels list under every filter.
-- They now list under the "From downloads" pill.
--
-- added_at stays NULL on purpose: these channels are visible, but not added,
-- so nothing here makes them a scan target. name comes from the video rows so
-- the list is not a wall of blanks before the metadata refresher gets to them;
-- MAX(NULLIF(...)) picks any non-empty name among the channel's videos.
INSERT OR IGNORE INTO channels (id, name)
SELECT v.channel_id, COALESCE(MAX(NULLIF(v.channel_name, '')), '')
FROM videos v
WHERE v.channel_id <> '' AND v.status = 'downloaded'
GROUP BY v.channel_id;
