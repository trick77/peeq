-- The player used to show format_used -- the resolved yt-dlp -f selector --
-- as a pill, so a user looking for "what is this file" read
-- "bestvideo[height<=1080][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4".
-- That string describes what was ASKED FOR, not what arrived: it is the same
-- for every video downloaded under the same preset, and says nothing about
-- the file on disk. format_used stays (it is the record of the request), but
-- these columns are what the file actually is, read from it with ffprobe.
--
-- Values are ffprobe's own ("h264", "aac", 1080). The friendly wording
-- ("H.264", "AAC", "1080p") lives in the UI so it can change without a
-- migration.
ALTER TABLE videos ADD COLUMN media_container TEXT NOT NULL DEFAULT '';
ALTER TABLE videos ADD COLUMN video_codec TEXT NOT NULL DEFAULT '';
ALTER TABLE videos ADD COLUMN video_height INTEGER NOT NULL DEFAULT 0;
ALTER TABLE videos ADD COLUMN audio_codec TEXT NOT NULL DEFAULT '';

-- probed_at records the ATTEMPT, not the success: it is stamped on every
-- exit of the probe, including the failure path. Without that the backfill
-- sweep would re-probe an unreadable or deleted file on every boot forever.
-- NULL therefore means exactly one thing -- "never attempted" -- which is
-- what the sweep selects on. This is the same rule the channel-metadata
-- resolver follows for resolve_ok.
ALTER TABLE videos ADD COLUMN probed_at TEXT;
