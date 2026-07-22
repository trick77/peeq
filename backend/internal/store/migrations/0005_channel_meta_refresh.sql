-- next_meta_refresh_at: when this subscription's channel metadata (name,
-- @handle, description, avatar, banner, subscriber count, verified flag) is
-- next due to be re-read from YouTube.
--
-- Until now that metadata was read exactly ONCE, on the first visit to a
-- channel page, and never again: 0001 stamps resolved_at even when the fetch
-- FAILS so an unresolvable channel is not re-fetched on every page load, which
-- also means a channel that resolved cleanly never gets a newer copy either.
-- Renames, new artwork, changed subscriber counts and lost checkmarks were
-- invisible until someone pressed Refresh by hand.
--
-- The column lives on subscriptions, not channels, because the weekly rotation
-- is subscribed-only — and because next_scan_at, the schedule it mirrors,
-- already lives here.
ALTER TABLE subscriptions ADD COLUMN next_meta_refresh_at TEXT;

-- The backfill is the whole anti-batch mechanism, so it is worth being precise
-- about why it is random rather than a plain datetime('now', '+7 days').
--
-- Every existing subscription shares the same "now" at migration time. Give
-- them all the same due date and they refresh together, all become due again
-- exactly one interval later, and stay locked in that convoy forever — a
-- weekly stampede of yt-dlp calls landing on whichever day this migration
-- happened to run. Seeding each row at a random minute inside the next 7 days
-- (10080 minutes) spreads them once, at the start; a fixed interval applied
-- from there keeps them spread.
--
-- (random() & 0x7fffffff) rather than abs(random()): abs() of the minimum
-- int64 overflows and errors in SQLite, and masking the sign bit off cannot.
UPDATE subscriptions
   SET next_meta_refresh_at = datetime('now', '+' || ((random() & 0x7fffffff) % 10080) || ' minutes');
