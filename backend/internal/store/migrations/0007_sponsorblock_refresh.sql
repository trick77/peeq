-- sponsorblock_refreshed_at: when this video's SponsorBlock segments were
-- last read from sponsor.ajay.app. Empty string = never.
--
-- Until now segments arrived exactly one way: parsed out of a download's own
-- info.json. Two kinds of video were therefore permanently without them —
-- everything imported from TubeArchivist (no peeq download ever ran) and
-- everything downloaded before the parser was fixed (it read the wrong
-- info.json key and stored an empty list for every video). Neither can be
-- repaired without re-downloading the media, which is absurd for data that is
-- a single HTTP request away.
--
-- The column drives the backfill worker's claim order: never-fetched videos
-- first, then the oldest reads. It is stamped even when the answer is "no
-- segments", so a video with nothing to skip is not re-asked on every pass.
--
-- Default '' rather than NULL keeps it consistent with the other TEXT
-- timestamp columns in this schema, and makes every existing row — including
-- every imported one — immediately eligible for the backfill.
ALTER TABLE videos ADD COLUMN sponsorblock_refreshed_at TEXT NOT NULL DEFAULT '';

-- Videos already carrying segments are still queued for a refresh rather than
-- stamped as done: the ones written before this release came from the broken
-- parser and are empty lists, and a genuinely populated one loses nothing by
-- being re-read once. The refresh is idempotent.
--
-- Claim order is oldest-refresh-first, so no random spreading is needed here
-- (unlike 0005's channel rotation): the backfill is a one-time drain against a
-- public API that is not rate-limit sensitive, not a recurring convoy of
-- yt-dlp calls against a Google account.
CREATE INDEX IF NOT EXISTS idx_videos_sponsorblock_refresh
    ON videos (sponsorblock_refreshed_at)
    WHERE status = 'downloaded';
