-- subtitles_default: the global "start videos with subtitles showing"
-- preference, read and written by the player's subtitles toggle and the
-- Playback section in Settings. Player-side only — nothing in the download
-- path reads it.
--
-- 0 (off) matches the behaviour peeq had before this was a setting, so an
-- existing library is unchanged by this migration.
ALTER TABLE settings ADD COLUMN subtitles_default INTEGER NOT NULL DEFAULT 0;
