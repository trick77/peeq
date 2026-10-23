-- 0023: move every remaining asset except the video file itself into the
-- database.
--
-- 0022 did this for the poster, for a reason that generalises: an asset stored
-- as a FILE plus a COLUMN pointing at it is two things that can drift apart,
-- and nothing in the schema stops them. The poster drifted because Upsert
-- blanked the pointer; the transcript is one tombstone rule away from the same
-- fate, and the channel artwork has no delete path at all.
--
-- The transcript is the one that matters most. transcript_chunks / fts_chunks /
-- vec_chunks answer every search, and the .vtt file is the ONLY thing they can
-- be rebuilt from — which is why the tombstone had to learn to spare .vtt
-- sidecars by name (#239) and why Reprocess refuses when subtitle_path is
-- empty. With the text in a row, a rebuild cannot be blocked by a missing file
-- and neither of those special cases has anything left to protect.
--
-- Side tables, not columns: every whole-row read (videoColumns, channelColumns)
-- selects the lot, and a BLOB or a 200 KB transcript there would ride along on
-- every list and search query that only wanted a title.
--
-- No data moves here — SQL cannot read a file. The diskimport worker carries
-- the existing files in and unlinks each one it has stored, so the media tree
-- converges on .mp4 files (plus .staging/ mid-download). The path columns stay
-- for now as that worker's map of where to look; they are dropped once it has
-- run in production and been deleted.

-- The transcript, stored as the raw WebVTT text yt-dlp produced.
--
-- Verbatim rather than parsed cues: the <track> element, the browser-side
-- parser in ui/src/vtt.tsx and the user-facing .vtt download all want the bytes
-- exactly as they were, so the two endpoints keep emitting a byte-identical
-- body and no parser has to change.
--
-- source is what the .summaries/ path prefix used to say. A caption fetched to
-- decide whether a video is worth downloading takes a deliberately truncated
-- analysis (no category, no key points, no embeddings), and the code told that
-- case apart by looking at the directory name in subtitle_path. With the file
-- gone the provenance has to be recorded rather than inferred.
--
-- No lang column: videos.audio_language already holds it, written in the same
-- statement as subtitle_path.
CREATE TABLE video_transcripts (
    video_id   TEXT PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE,
    source     TEXT NOT NULL CHECK (source IN ('download','caption')),
    vtt        TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Channel avatars and banners. These leaked outright before: no code path ever
-- removed .channels/<UCID>/, so deleting a channel left its artwork on disk
-- forever, and a refresh that returned a different content type orphaned the
-- old-extension file beside the new one. The cascade settles both.
CREATE TABLE channel_images (
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('avatar','banner')),
    mime       TEXT NOT NULL,
    bytes      BLOB NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (channel_id, kind)
);

-- Inbox posters: the cached copy of a not-yet-downloaded video's thumbnail, so
-- the browser never loads it from YouTube's CDN directly.
--
-- The cascade here is a BACKSTOP, not the reclaim path: a channel_videos row is
-- not deleted when the user approves, queues or ignores an inbox item — its
-- state flips and the row stays — so the explicit delete on those three
-- transitions is still what frees the space. What the cascade fixes is channel
-- deletion, which orphaned .pending/<id>/ the same way .channels/ was orphaned.
CREATE TABLE pending_thumbnails (
    video_id   TEXT PRIMARY KEY REFERENCES channel_videos(video_id) ON DELETE CASCADE,
    mime       TEXT NOT NULL,
    bytes      BLOB NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
