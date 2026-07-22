-- channel_metadata: the facts YouTube publishes about a channel, which the
-- channel page now shows next to peeq's own archive stats.
--
-- All three come from the metadata-only yt-dlp call peeq ALREADY makes when
-- it resolves a channel (channel_follower_count / channel_is_verified in the
-- response) — no extra request, and no cookie needed for either field.
--
-- resolve_ok is the important one. resolved_at is stamped even when a resolve
-- FAILS (see the channels table comment in 0001) so an unresolvable channel is
-- not re-fetched on every page visit — which means resolved_at alone cannot
-- tell "refreshed, all good" apart from "tried once, failed, gave up". The
-- channel page needs that difference: the second case is why a channel sits
-- there with no avatar, no banner and no description, and it is what the
-- Refresh button exists to fix.
--
-- Defaults are the honest reading of an existing row: 0 subscribers means
-- "not known yet", and resolve_ok = 0 means "peeq has not (yet) recorded a
-- successful resolve", which is exactly true of every row written before this
-- migration. A channel is re-marked ok the next time it resolves.
ALTER TABLE channels ADD COLUMN subscriber_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE channels ADD COLUMN verified INTEGER NOT NULL DEFAULT 0;
ALTER TABLE channels ADD COLUMN resolve_ok INTEGER NOT NULL DEFAULT 0;
