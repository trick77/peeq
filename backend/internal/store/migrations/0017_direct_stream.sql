-- Direct playback grants: the auth-free stream URLs AirPlay needs.
--
-- AirPlay on a plain <video> does not proxy bytes through the browser: Safari
-- hands the media URL to the receiver, which fetches it itself with no session
-- cookie. /api/videos/{id}/stream is session-gated, so an Apple TV gets a 401
-- and playback silently fails.
--
-- Serving media at a public /videos/{id}/... path is not an option: peeq's
-- video id IS the YouTube id, so such a route would be walkable by anyone who
-- can read a YouTube link. A grant is the alternative — an opaque 32-byte
-- token that names one video, expires, and lives in a namespace entirely
-- separate from share_links so holding one never reveals the other.
--
-- direct_stream_enabled defaults to 0: the feature is opt-in, so an existing
-- install keeps exactly today's behaviour (every media byte session-gated)
-- until the owner turns it on. The handler re-reads the flag per request, so
-- switching it off revokes every outstanding grant at once.
ALTER TABLE settings ADD COLUMN direct_stream_enabled INTEGER NOT NULL DEFAULT 0;

-- Two deliberate differences from share_links (0008):
--
--   * No UNIQUE index on video_id. A share link is a durable thing the owner
--     hands out and re-reads from the popover, so there is exactly one per
--     video. Grants are disposable — minted whenever the player opens a video
--     — and several may legitimately be live at once (two browsers, a reload
--     during playback). Uniqueness would make each new tab kill the previous
--     tab's stream.
--   * expires_at is NOT NULL. A share link may live forever by design; a grant
--     always expires, and that bound is what makes an auth-free URL acceptable.
CREATE TABLE playback_grants (
    token      TEXT PRIMARY KEY,
    video_id   TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_playback_grants_video ON playback_grants(video_id);
