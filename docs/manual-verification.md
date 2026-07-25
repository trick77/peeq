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
   in **Decide** on the next scan. **Download now** should enqueue it (it moves to the **Queue**
   page, where progress is visible, and the rail's status panel names the stage) and it should
   land in Library as `downloaded`. **Ignore** on another item should remove it from the list.
   With several items from one channel present, the **channel filter chips** and the **Download
   all** action (which confirms before a large batch) should queue every visible item.
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

## Channel page

Run after any change to the channel page, the `channels` table, or the scan scheduler.

1. Track a channel with loud, busy channel art. On its page, confirm the banner reads as a
   **backdrop**: the name, handle, description and stats stay fully legible on top of it. If they
   do not, lower `.chan-banner`'s opacity rather than changing the palette.
2. Confirm the four header stats are right: archived count, runtime, disk used, and the age of the
   newest video. They count **downloaded** videos only — a queued or errored video must not be
   counted as archived.
3. Track a channel that has no banner, and one whose avatar failed to fetch. The header must fall
   back to the per-id gradient without collapsing its layout.
4. Click a channel name from each of the four entry points — the Channels row, a Library card, the
   player, and a Decide card — and confirm each opens that channel.
5. Open a channel you never tracked (click the channel name on a video you added by URL). The
   Archive tab lists its videos, the New and Settings tabs are absent, and the header offers
   **Track this channel**. Reload: the avatar, banner and description have filled in from the
   background resolve.
6. On the Settings tab press **Check now** and confirm it says "Checking soon", never implying the
   scan already ran. Then break it deliberately — pause YouTube in Settings, or let the cookie go
   stale — press it again, and confirm the page shows the reason instead of appearing to do nothing.
7. Confirm the New tab's empty state shows the last-checked and next-check times. Remember this is
   the normal state for a channel with auto-add on: discoveries are enqueued immediately and never
   become pending.
8. On the Channels page, confirm the counts line under each channel name is faint and one step
   smaller than the name, with only the numerals lifted — readable, but not competing with the name.
9. Narrow the window to phone width. The header wraps, the buttons drop below the stats, and the
   page body does not scroll sideways.

## Now playing across two clients

Run after any change to the resume/watched write path, `videos.state_version`, or the
`playback_state` pointer. Needs two clients on the same peeq — two browsers, or one plus a
private window, both signed in.

1. **The pointer follows you.** In client A open a video from the Library and let it play a
   minute. Confirm `sqlite3 data/peeq.db "select * from playback_state"` shows that id.
2. In client B load `/`. It must land on the **Library**, not in a player — the pointer is the
   rail's fallback, not a redirect.
3. In client B click **Now playing** in the rail. The same video opens, at A's position, and the
   address bar reads `/video/<id>` — not `/video`.
4. **The watched button reads as watched.** In the player, confirm the button is plain grey while
   unwatched and turns terracotta-tinted the instant you click it, with the label flipping to
   "Mark unwatched". It must stay clearly distinct from the gold **Kept forever** button beside
   it.
5. Confirm marking it watched clears the pointer: client B's rail click (or a reload) now shows
   "Nothing playing", and the table's `video_id` is NULL.
6. **Issue #97.** Open the *same* video in both clients and leave both playing. In A, mark it
   watched. Within five seconds, B's Network panel shows `POST /api/videos/<id>/resume` answered
   **409**, followed by a `GET /api/videos/<id>`; B then pauses, rewinds to 0:00, and shows
   "Marked watched on another device." Then confirm the row agrees with itself:
   `select watched, resume_position_seconds from videos where id='<id>'` must be `1|0.0`, never
   `1` with a non-zero position.
7. Confirm the guard never fires against a single client: in one tab, seek past 90% and let two
   resume pings go by. Both must be 200, with no toast — auto-watch bumps the version and the
   response hands the new one straight back.
8. Delete the video and confirm `GET /api/playback` reports an empty `video_id`, even though the
   row survives as `status='tombstoned'`.
