-- Inbox summaries: read a video before deciding whether to download it.
--
-- The Inbox shows a poster, a title, a channel and a runtime, which is not
-- enough to judge a 42-minute video. Captions can be fetched on their own for a
-- few KB (yt-dlp --skip-download --write-subs), and the summarizer already
-- takes a .vtt rather than a media file — so every newly discovered video can
-- carry a ~190-word summary before anyone looks at it.
--
-- Three columns, and one UPDATE that is load-bearing.

-- Per-channel opt-out. Default 1: a channel is subscribed to because its videos
-- are worth considering, and a summary is what makes considering them cheap.
-- The switch exists for the channel subscribed to for one thing in ten, where
-- nine summaries a week are pure cost.
ALTER TABLE channels ADD COLUMN auto_summary INTEGER NOT NULL DEFAULT 1;

-- caption_attempts / next_caption_attempt_at drive the retry ladder.
--
-- YouTube's automatic captions do not exist at publish time — the ASR pass runs
-- minutes to hours after the upload appears, longer for long videos. So the
-- common outcome for a video discovered promptly is "no captions yet", which is
-- an ordinary state and not a failure. The fetcher tries at discovery and then
-- at +15m, +1h, +6h, +24h before giving up and marking the video no_transcript.
--
-- The counter lives on the ledger rather than on videos because it is a
-- property of the attempt to learn about a video, not of the video: a row that
-- never yields captions never gets a videos row worth keeping.
ALTER TABLE channel_videos ADD COLUMN caption_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE channel_videos ADD COLUMN next_caption_attempt_at TEXT;

-- This is how "from now on only" is enforced, and it is the whole reason this
-- migration is not just three ALTERs.
--
-- Without it, deploying this feature would hand the caption fetcher every video
-- already sitting in the Inbox — for a long-neglected inbox, hundreds of
-- yt-dlp calls and hundreds of LLM summaries, all at once, for videos the user
-- has already scrolled past without downloading. The user asked for new videos
-- only, and 99 is past the ladder's limit of 5, so every row that exists right
-- now is born exhausted.
--
-- Deliberately unguarded by state: an 'ignored' or 'queued' row is retired
-- too. A row can return to 'pending' (0014's unavailable -> pending
-- re-promotion does exactly that), and such a row is not a new video either —
-- it is an old one that became visible again.
--
-- A sentinel rather than a NULL next_caption_attempt_at, because "never try"
-- and "try as soon as possible" both want that column empty; only the counter
-- can tell them apart.
--
-- HISTORY, and the reason 0021 does not exist: this line was undone once, by a
-- 0021_backfill_inbox_summaries.sql that reset the counter to 0 for every
-- 'pending' row with no videos row — that is, every inbox video peeq had never
-- looked at. It was a deliberate one-off: the feature shipped reading only new
-- videos, and once it had proven itself the pre-existing inbox was read too.
--
-- That migration was deleted after it had run everywhere, so the numbering
-- skips from here to 0022. The deletion is safe because schema_migrations
-- records applied migrations by FILENAME (see Migrate) with no checksum and no
-- count: a database that ran 0021 keeps the row and never notices the file is
-- gone, and a fresh database skips an UPDATE that would have matched nothing.
--
-- The one case it does not cover, stated so nobody has to rediscover it:
-- restoring a backup taken BEFORE 0021 ran. Such a database is back to being
-- retired by the line below, with nothing left to undo it, and its old inbox
-- stays unread. Re-running that one UPDATE by hand is the whole fix.
UPDATE channel_videos SET caption_attempts = 99;

-- No index for the fetcher's claim query, deliberately.
--
-- The obvious one is partial — WHERE caption_attempts < 5, since the rows the
-- UPDATE above just retired are the overwhelming majority on any existing
-- install and none of them can ever match again. But the query binds that
-- bound as a parameter (it comes from channelvideos.CaptionMaxAttempts), and
-- SQLite will not use a partial index unless it can prove the WHERE clause
-- implies the index predicate, which it cannot do against a parameter. The
-- index would sit there costing writes and never be chosen.
--
-- The unqualified alternative is not worth it either: state already has
-- idx_channel_videos_state from 0001, the ledger is thousands of rows rather
-- than millions, and this query runs once a minute on one goroutine.
