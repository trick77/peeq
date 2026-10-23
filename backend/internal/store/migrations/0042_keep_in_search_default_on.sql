-- keep_reads now decides what survives a video's file for every video of the
-- channel, not only for inbox reads: on, a delete tombstones (the file goes,
-- the transcript, summary and search index stay); off, it purges the row and
-- everything hanging off it. With that meaning, off by default would purge the
-- searchable memory of every watched video on the next retention sweep, so
-- every channel is turned on and on becomes the default.
--
-- SQLite cannot change a column default in place, and rebuilding channels is
-- not an option: with foreign_keys on, DROP TABLE fires every ON DELETE CASCADE
-- hanging off it (subscriptions, the channel_videos ledger, channel art).
-- Dropping and re-adding the one column does both jobs at once: the re-added
-- column fills every existing row with the new default.
ALTER TABLE channels DROP COLUMN keep_reads;
ALTER TABLE channels ADD COLUMN keep_reads INTEGER NOT NULL DEFAULT 1;

-- Turning the switch off purges what it kept: the channel's tombstones and its
-- kept inbox reads (videos.Store.KeptVideos). That query needs channel_id,
-- status and id, and a videos row keeps id behind its big text columns, so
-- without id in the index it walks overflow pages per row.
-- idx_videos_channel_status (0032) lacks id; videos/queryplan_test.go pins it
-- as the plan the Library must NOT take, so it stays as it is.
--
-- Partial, on exactly the two statuses the query reads. A full index here is
-- the narrowest one for other queries too: with channel_id first the planner
-- took it away from idx_videos_channel_search for the channel page's
-- searchable count (channels/queryplan_test.go); with status first, any
-- status-only query could pick it. A partial index is only usable by a query
-- whose WHERE implies its own, so neither can.
CREATE INDEX idx_videos_channel_kept ON videos (channel_id, status, id)
    WHERE status IN ('tombstoned', 'new');
