-- share_links: a public, unauthenticated door to a single downloaded video.
-- peeq is single-user and every other route sits behind OIDC; a share link is
-- the one exception — an opaque, optionally-expiring token that lets someone
-- WITHOUT an account stream one video (and read its summary/highlights) on the
-- chromeless /s/<token> page.
--
-- Unlike sessions and the machine api token, the raw token is stored here, not
-- just its hash. Those are owner/machine credentials whose only job is to prove
-- identity, so a DB leak must not yield a reusable secret. A share token is the
-- opposite: it is a CAPABILITY URL, deliberately handed out in plaintext (pasted
-- into a chat, an email, a browser history) and it must be re-displayable so the
-- owner can copy it again from the share popover. Redisplay is impossible from a
-- one-way hash, so the token itself lives here. The blast radius is bounded by
-- design — a leaked token grants read to exactly the videos already chosen for
-- sharing, and only until it expires or is revoked.
--
-- UNIQUE(video_id) enforces one live link per video. Re-sharing a video that
-- still has a LIVE link keeps that token and only re-stamps its expiry (see
-- Store.Upsert); a new token is minted only once the previous one has expired or
-- been revoked. Deleting the row (Stop sharing) is what kills a link
-- immediately. expires_at NULL means "never expires"; a non-null value is a UTC
-- 'YYYY-MM-DD HH:MM:SS' datetime compared against datetime('now') on resolve,
-- mirroring the sessions table.
--
-- ON DELETE CASCADE ties the link's life to the video's: deleting the video
-- drops its share link too. This SHOULD cascade (unlike activity_events, which
-- must outlive its subject) — a link to a row that no longer exists is dead
-- weight, and the public handler treats a missing video as a 404 regardless.
CREATE TABLE share_links (
    token      TEXT PRIMARY KEY,
    video_id   TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    expires_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- One live link per video: the upsert in Store.Create targets this constraint.
CREATE UNIQUE INDEX idx_share_links_video ON share_links(video_id);
