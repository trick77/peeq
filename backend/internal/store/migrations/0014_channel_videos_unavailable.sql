-- 0014: give the scan ledger a re-checkable "unavailable" state, so a video
-- peeq cannot download right now (members-only, age-gated, geo-blocked,
-- private) leaves the inbox WITHOUT being buried forever.
--
-- The bug this closes: the inbox's Download button flips a row to 'queued' at
-- CLICK time, before yt-dlp has run. Nothing anywhere writes 'pending' back,
-- so a download that then fails terminally left the item with no way home —
-- it was gone from the inbox and sitting in the Library as a broken 'error'
-- row whose re-download button could never succeed.
--
-- 'ignored' is NOT the right resting place for these. Ledger.Exists matches on
-- video_id with no state predicate, so any state a scan does not revisit is
-- terminal: a members-only video that the channel later makes public would
-- never come back. 'unavailable' is revisited on every scan pass and returns
-- to 'pending' the moment the listing shows it is reachable again.
--
-- unavailable_reason carries the ytdlp.TerminalError reason (members / age /
-- geo / private / deleted) so the UI and the logs can say WHY, and so a future
-- pass can treat "deleted" differently from "members" without re-deriving it.
-- It is '' for every other state.
--
-- unavailable_at is when the row was last CONFIRMED unavailable, and it exists
-- because the scan listing usually cannot tell us anything: yt-dlp's flat
-- entries carry `availability` only when the tab card renders a badge, so "no
-- gate flag" is silence, not proof either way. Reviving on silence would bounce
-- the row into the inbox on every pass; burying on silence would lose a video
-- the channel later made public.
--
-- So the row only ever moves on evidence. This timestamp is not a re-offer
-- timer — it is a rate limiter on the thing that PRODUCES the evidence: a
-- per-video yt-dlp metadata call, which reports availability reliably. At most
-- one such probe per video per unavailableRecheckWindow, and at most a few per
-- scan pass. Any answer (public → back to the inbox; still gated → stay) resets
-- the clock; a probe that fails to answer leaves it untouched, so a run of
-- network trouble cannot quietly postpone the next real check.
--
-- SQLite cannot widen a CHECK constraint in place, so this is the standard
-- 12-step rebuild. Both indexes are dropped with the old table and recreated
-- by name below. Nothing in the schema has a foreign key TO channel_videos
-- (its own FK to channels is outgoing), so the drop/rename is safe with
-- foreign_keys ON, which is how the migration runner's connection is opened.
CREATE TABLE channel_videos_new (
    video_id           TEXT PRIMARY KEY,
    channel_id         TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    title              TEXT NOT NULL DEFAULT '',
    duration_seconds   INTEGER,
    url                TEXT NOT NULL DEFAULT '',
    thumbnail_url      TEXT NOT NULL DEFAULT '',   -- REMOTE url (not a local path)
    state              TEXT NOT NULL DEFAULT 'seen' CHECK (state IN ('seen','pending','ignored','queued','unavailable')),
    discovered_at      TEXT NOT NULL DEFAULT (datetime('now')),
    decided_at         TEXT,
    published_at       TEXT,
    unavailable_reason TEXT NOT NULL DEFAULT '',
    unavailable_at     TEXT
);

INSERT INTO channel_videos_new
    (video_id, channel_id, title, duration_seconds, url, thumbnail_url,
     state, discovered_at, decided_at, published_at)
SELECT
     video_id, channel_id, title, duration_seconds, url, thumbnail_url,
     state, discovered_at, decided_at, published_at
FROM channel_videos;

DROP TABLE channel_videos;
ALTER TABLE channel_videos_new RENAME TO channel_videos;

CREATE INDEX idx_channel_videos_channel ON channel_videos(channel_id);
CREATE INDEX idx_channel_videos_state ON channel_videos(state);

-- Rescue the rows this bug already stranded. A video whose ledger row says
-- 'queued' but whose videos row failed terminally is exactly the case above:
-- move it to 'unavailable' so the next scan re-checks it, and carry the
-- reason across from the recorded error message.
--
-- The LIKE patterns match ytdlp.TerminalError's Error() text ("ytdlp: terminal
-- (members)"), which is what the download worker writes into
-- videos.error_message. Rows whose error was anything else (a network failure,
-- an exhausted retry) are deliberately left alone: those ARE re-downloadable,
-- and the Library's re-download button is the right recovery for them.
UPDATE channel_videos
SET state = 'unavailable',
    unavailable_at = datetime('now'),
    unavailable_reason = CASE
        WHEN (SELECT v.error_message FROM videos v WHERE v.id = channel_videos.video_id) LIKE '%(members)%' THEN 'members'
        WHEN (SELECT v.error_message FROM videos v WHERE v.id = channel_videos.video_id) LIKE '%(age)%'     THEN 'age'
        WHEN (SELECT v.error_message FROM videos v WHERE v.id = channel_videos.video_id) LIKE '%(geo)%'     THEN 'geo'
        WHEN (SELECT v.error_message FROM videos v WHERE v.id = channel_videos.video_id) LIKE '%(private)%' THEN 'private'
        ELSE 'deleted'
    END
WHERE state = 'queued'
  AND video_id IN (
      SELECT v.id FROM videos v
      WHERE v.status = 'error' AND v.error_message LIKE 'ytdlp: terminal (%'
  );

-- ...and drop the broken Library rows those tickets now stand for. The ledger
-- row above is the durable memory of the video, so deleting the videos row
-- loses nothing recoverable — it only removes a card that offers a
-- re-download which cannot ever work. Scoped to rows the ledger just adopted,
-- so a manually-added video (which has no ledger row to remember it) keeps its
-- error row and stays visible.
DELETE FROM videos
WHERE status = 'error'
  AND error_message LIKE 'ytdlp: terminal (%'
  AND id IN (SELECT video_id FROM channel_videos WHERE state = 'unavailable');
