-- playback_state is the server-side "now playing" pointer: which video the user
-- last opened in the Player. Without it, "now playing" lives only in the SPA's
-- in-memory route state, so it dies on reload and differs on every device -- open
-- a video at the desk and the couch's rail shows "Nothing playing".
--
-- A NEW singleton table rather than a column on `settings`, deliberately:
-- `settings` is user-configured preference (format presets, retention, the
-- cookie) that the Player GETs on every mount for subtitles_default. This is
-- mutable session state, written on every Player mount and cleared by a watched
-- toggle -- a different lifecycle, and it must not be reachable from the
-- Settings page's PUT surface.
--
-- Singleton (id = 1) because peeq is single-user: the users table holds one row
-- and sessions.user_id is the only user reference anywhere in the schema. If
-- peeq ever grows real multi-user, this becomes (user_id PRIMARY KEY) and the
-- CHECK goes away -- a rename of the key, not a reshape of the feature.
--
-- ON DELETE SET NULL covers a hard row delete. It does NOT cover the normal
-- delete path: videos.Tombstone keeps the row and sets status='tombstoned', so
-- the FK never fires. playback.Store.Get therefore filters on
-- status='downloaded' and reports an empty pointer rather than sending the rail
-- into a dead player.
CREATE TABLE playback_state (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    video_id   TEXT REFERENCES videos(id) ON DELETE SET NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Seeded here, like settings in 0001_init.sql, so Get is a plain QueryRow with
-- no "row missing" special case and Set never has to upsert.
INSERT INTO playback_state (id, video_id) VALUES (1, NULL);
