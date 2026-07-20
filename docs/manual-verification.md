# Manual verification checklists

These flows need a real YouTube cookie and real AI endpoints, so they are not covered by automated
tests. Run the relevant checklist by hand after any change that touches the areas below.

## Channels & subscriptions

Run after any change to channels, scanning, or the download pipeline.

1. Boot with a real DB/media dir; sign in; paste a real cookie in Settings and confirm status
   shows "active".
2. **Add** a channel by `@handle` under Channels — it should appear tracked with the resolved
   channel name. **Subscribe** to it.
3. Wait for (or force) the scheduler's first pass on that channel — it should show `baselined_at`
   set and queue **nothing** (first-run baseline, no backfill).
4. Once a genuinely new upload exists on a subscribed, non-autodownload channel, confirm it lands
   in **Pending** on the next scan. **Download now** should enqueue it (progress visible in
   the dock) and it should land in Library as `downloaded`. **Ignore** on another pending item
   should remove it from the list.
5. Flip a channel to **Autodownload** with a format override — the next new upload on that channel
   should enqueue automatically at low priority and download using the override format.
6. Paste a **video** URL (not a channel URL) into Add — confirm its channel is silently tracked
   (appears under Channels → Tracked) without being subscribed.
7. **Delete** a channel that has a downloaded, favorited video — confirm the video row and its
   media file are both gone (cascade delete overrides the favorite), and that deleting a channel
   mid-download cancels the running job for that channel.
8. With multiple subscribed channels, watch the logs across a scan cycle and confirm at least 60s
   between channels and a 20s+ delay per yt-dlp call.

## Captions, summaries & search

Run after any change to captions, embeddings, summaries, chapters, or global search.

1. Boot with a real YouTube cookie, a running **chat endpoint** (`BACKEND_CHAT_BASE_URL` +
   `BACKEND_CHAT_API_KEY`), and a running **embeddings endpoint** (`BACKEND_EMBED_BASE_URL` +
   `BACKEND_EMBED_API_KEY` + `BACKEND_EMBED_MODEL` + `BACKEND_EMBED_DIM`). peeq will refuse to
   start without the base URLs and the embedding model.
2. Download a real video and confirm its captions are present:
   - Check that VTT captions are extracted (if the video has YouTube-hosted captions or subtitles).
   - Confirm CC (closed captions) file(s) are generated and stored alongside the video.
3. Wait for artifact generation (summaries, chapters, highlights) to complete. Verify all three
   appear in the video detail panel.
4. Perform a **global search** (search box, top of page) using a keyword from the video's transcript.
   Confirm the result appears and links to the correct timestamp within the video.
