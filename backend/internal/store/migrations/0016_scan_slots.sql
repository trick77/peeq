-- Put every existing subscription onto its slot, once, so the fix is visible on
-- the first day rather than on the second.
--
-- Both schedules used to be anchored to when the previous run FINISHED
-- (next_scan_at = now + 24h ± 3h, next_meta_refresh_at = now + 7d ± 12h). That
-- has no way back from a convoy: an expired cookie, a restart or the kill-switch
-- makes the whole fleet due at once, the scheduler drains it one channel per
-- minute, and each channel then re-anchors to that burst — so the clump renews
-- itself every cycle and keeps its time of day forever. Production showed 44
-- channels scanning within a few minutes of each other instead of one every 33.
--
-- The code now targets a slot instead: rank * interval / count, counted from the
-- Unix epoch, recomputed from current membership on every reschedule (see
-- sched.Slot and scan.NextScanAt). That is self-healing — after any outage each
-- channel returns to its own slot on the next cycle. This backfill only spares
-- the deployment its one remaining convoy, by moving the rows already clumped.
--
-- 0005 did the same job for next_meta_refresh_at with a random minute inside the
-- next 7 days. Random was the right tool then, because nothing downstream held
-- the spread; now something does, so this seeds the exact slots the schedulers
-- will keep using rather than a scatter they would have to inherit.
--
-- The arithmetic mirrors sched.NextSlotAfter exactly, in seconds:
--
--   slot   = rank * period / total          -- seconds into the cycle
--   base   = now + period/2                 -- the half-interval floor
--   behind = (base - slot) mod period       -- back to the last slot before base
--   due    = base - behind + period         -- so it is STRICTLY after base
--
-- The floor is what stops a channel whose slot falls a few minutes from now
-- being re-scanned immediately. Its price is that this one cycle lands somewhere
-- in 12–36h (scan) or 3.5–10.5 days (metadata); from then on every channel is
-- exactly on its interval.
--
-- `+ period * (... < 0)` is SQLite's boolean-as-1-or-0 giving a non-negative
-- remainder, which plain % does not for a negative left operand. It cannot fire
-- for these values — base is an epoch, slot is under a week — but the expression
-- has to mean the same thing as the Go it mirrors, not merely agree on the
-- inputs we happen to feed it.
--
-- strftime('%s') rather than unixepoch(): the latter needs SQLite 3.38 and buys
-- nothing here. ROW_NUMBER() (3.25) and UPDATE ... FROM (3.33) are both well
-- inside what the bundled build provides.
WITH ranked AS (
    SELECT channel_id,
           ROW_NUMBER() OVER (ORDER BY channel_id) - 1 AS rank,
           (SELECT COUNT(*) FROM subscriptions)        AS total,
           CAST(strftime('%s', 'now') AS INTEGER)      AS now_s
      FROM subscriptions
),
slotted AS (
    SELECT channel_id,
           now_s + 43200            AS scan_base,
           rank * 86400 / total     AS scan_slot,
           now_s + 302400           AS meta_base,
           rank * 604800 / total    AS meta_slot
      FROM ranked
),
due AS (
    SELECT channel_id,
           scan_base + 86400
             - ((scan_base - scan_slot) % 86400
                + 86400 * ((scan_base - scan_slot) % 86400 < 0))   AS scan_at,
           meta_base + 604800
             - ((meta_base - meta_slot) % 604800
                + 604800 * ((meta_base - meta_slot) % 604800 < 0)) AS meta_at
      FROM slotted
)
-- A pending "Check now" is the one thing this must not move. Its marker sits in
-- scan_requested_at with next_scan_at already in the past, waiting for the next
-- poll — and the realistic way a request is still pending at migration time is
-- exactly the outage this PR is about: the cookie expires, the scheduler is
-- gated, the user clicks Check now, the cookie is fixed and the container is
-- redeployed. Migrate runs before the workers, so an unconditional write would
-- push that request 12-36h out while Activity still shows it as requested.
-- Those rows keep their past next_scan_at, get scanned promptly, and that scan
-- puts them on their slot — the same self-healing every other path relies on.
--
-- Only the scan column is guarded: a pending scan request says nothing about
-- the metadata rotation, which is spread here for every row.
UPDATE subscriptions
   SET next_scan_at         = CASE WHEN subscriptions.scan_requested_at IS NULL
                                   THEN datetime(due.scan_at, 'unixepoch')
                                   ELSE subscriptions.next_scan_at END,
       next_meta_refresh_at = datetime(due.meta_at, 'unixepoch')
  FROM due
 WHERE due.channel_id = subscriptions.channel_id;
