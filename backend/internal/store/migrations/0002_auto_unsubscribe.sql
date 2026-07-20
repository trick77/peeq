-- 0002_auto_unsubscribe.sql — staleness tracking for subscriptions.

-- Consecutive scans that returned a terminal "deleted" for this channel.
-- ANY clean scan resets this to 0, so only a sustained run ever acts.
ALTER TABLE subscriptions ADD COLUMN dead_scan_count INTEGER NOT NULL DEFAULT 0;

-- When the user dismissed a dormancy flag. The flag re-arms only if a video
-- is discovered AFTER this instant and the channel then goes quiet again.
ALTER TABLE subscriptions ADD COLUMN dormant_dismissed_at TEXT;

-- The visible record of what peeq unsubscribed on its own. Outlives the
-- subscriptions row it replaces. The single-value CHECK is deliberate: a
-- future reason must widen it consciously, so the Go constant and the SQL
-- enum cannot drift apart silently.
CREATE TABLE auto_unsubscribes (
    channel_id TEXT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    reason     TEXT NOT NULL CHECK (reason IN ('deleted')),
    at         TEXT NOT NULL DEFAULT (datetime('now'))
);
