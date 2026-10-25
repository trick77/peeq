-- In-depth texts stored cut off go back to the retry queue. Until the in-depth
-- calls refused a "length" finish (llm.FailOnCut), a reasoning model that spent
-- its budget thinking left a fragment, and it was stored as the finished text:
-- 6 in production, one of them 22 words. Every whole text has at least one
-- "###" heading line (inDepthRules; the parser takes it with or without the
-- space), so a body with none is a fragment, and it carries no in-depth chunks
-- (no section, nothing was indexed).
--
-- Only videos the retry can rewrite: a finished summary, a transcript, not an
-- ignored read (the retry drops those without writing). Elsewhere the fragment
-- is all there is, and it stays.
--
-- A migration, not boot code: it must run once. A text that ignores the format
-- legitimately has no heading either, and re-queuing it at every boot would
-- loop on it forever.
--
-- Queue first, then delete: the retry writes only where no text exists. A
-- video already queued keeps its row and history.
CREATE TEMP TABLE cut_in_depth AS
SELECT d.video_id FROM video_in_depth d
JOIN videos v ON v.id = d.video_id
WHERE d.body NOT LIKE '%###%'
  AND v.summary_status = 'done' AND v.summary <> ''
  AND EXISTS (SELECT 1 FROM video_transcripts t WHERE t.video_id = v.id)
  AND NOT EXISTS (SELECT 1 FROM channel_videos cv WHERE cv.video_id = v.id AND cv.state = 'ignored');

INSERT OR IGNORE INTO in_depth_retry (video_id) SELECT video_id FROM cut_in_depth;

DELETE FROM video_in_depth WHERE video_id IN (SELECT video_id FROM cut_in_depth);

DROP TABLE cut_in_depth;
