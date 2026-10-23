-- activity_events: the durable "what the background workers did" log behind the
-- Activity page. peeq's six goroutines — channel scans, metadata refreshes,
-- downloads, summaries, retention sweeps, yt-dlp self-updates — leave almost no
-- trace today: a scan finds three uploads, the sweeper reclaims 4 GB, none of it
-- is visible after the fact. One terminal row per completed piece of automatic
-- work fixes that.
--
-- Deliberately NO foreign key to videos/channels. This log must OUTLIVE the row
-- it describes: the retention sweeper tombstones (and can hard-delete) a video,
-- and the whole point of the "download" / "summary" rows is to still say peeq
-- once had it. A cascade would make the sweeper erase its own audit trail. The
-- subject name is denormalized (frozen at write time) for the same reason —
-- there is nothing left to join against once the video is gone. This mirrors
-- auto_unsubscribes, which likewise outlives its subscriptions row.
--
-- The past half of the agenda reads from this table; the future half is a live
-- projection over the existing schedules/queues (subscriptions.next_scan_at,
-- pending jobs, …) and is never stored, so a schedule change can't leave a stale
-- copy here.
CREATE TABLE activity_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    at         TEXT NOT NULL DEFAULT (datetime('now')),
    -- kind widens only consciously, the auto_unsubscribes.reason precedent: a
    -- new source of automatic work must add itself to this CHECK on purpose.
    kind       TEXT NOT NULL CHECK (kind IN
                 ('scan','channel_meta','download','summary','retention','ytdlp','access')),
    outcome    TEXT NOT NULL CHECK (outcome IN ('ok','warn','fail')),
    subject_id TEXT NOT NULL DEFAULT '',
    subject    TEXT NOT NULL DEFAULT '',  -- display name, frozen at write time
    summary    TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT ''
);

-- No secondary index: INTEGER PRIMARY KEY IS the rowid, which SQLite already
-- walks in reverse for the only access pattern this table has — ORDER BY id DESC
-- and WHERE id <= ? (keyset pagination, newest first). Timestamps collide at the
-- 1-second datetime('now') resolution, so id is also the tiebreaker, never `at`.
