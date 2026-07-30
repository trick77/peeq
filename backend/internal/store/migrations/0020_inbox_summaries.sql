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
UPDATE channel_videos SET caption_attempts = 99;

-- The fetcher's claim query filters on state, auto_summary and the two columns
-- above. state already has an index (idx_channel_videos_state from 0001); this
-- makes the due-work lookup a range scan over the rows that can actually be
-- due, rather than a scan of every ledger row on every tick. Partial, because
-- the exhausted rows the UPDATE above just created are the overwhelming
-- majority on any existing install and none of them will ever match.
CREATE INDEX idx_channel_videos_caption_due
    ON channel_videos(next_caption_attempt_at)
 WHERE caption_attempts < 5;
