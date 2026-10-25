-- In-depth texts stored cut off go back to the retry queue. Until the in-depth
-- calls refused a "length" finish (llm.FailOnCut), a reasoning model that spent
-- its budget thinking left a fragment, and it was stored as the finished text:
-- 6 in production, one of them 22 words. Every whole text has at least one
-- "### " section line (inDepthRules), so a body without one is a fragment.
--
-- A migration, not boot code: it must run once. A text that ignores the format
-- legitimately has no section either, and re-queuing it at every boot would
-- loop on it forever.
--
-- Queue first, then delete: the retry writes only where no text exists. A
-- video already queued keeps its row and history. Such texts carry no
-- in-depth chunks (no section, nothing to index), so the index needs nothing.
INSERT OR IGNORE INTO in_depth_retry (video_id)
SELECT video_id FROM video_in_depth WHERE body NOT LIKE '%### %';

DELETE FROM video_in_depth WHERE body NOT LIKE '%### %';
