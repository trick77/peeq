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

## TubeArchivist import — Phase A (subscriptions)

Requires a live TubeArchivist instance, so it cannot be covered by automated
tests. Get an API token from TubeArchivist's settings UI, or
`GET /api/appsettings/token/`.

**1. Survey — writes nothing.**

```bash
docker compose run --rm peeq import-ta-channels \
  --ta-url http://tubearchivist:8000 \
  --ta-token "$TA_TOKEN" \
  --dry-run
```

Check:
- The subscription count matches TubeArchivist's own subscribed-channel list.
- The "Skipped" count is plausible. These are channels TubeArchivist knows only
  because a video was downloaded from them once; they are deliberately not
  imported.
- Any inactive channels listed are ones you recognise as dead.

**2. Real run, with peeq stopped.**

```bash
docker compose stop peeq
docker compose run --rm peeq import-ta-channels \
  --ta-url http://tubearchivist:8000 \
  --ta-token "$TA_TOKEN"
docker compose start peeq
```

peeq must be stopped because the subcommand writes to the same SQLite database.

**3. Verify in the UI.**

- The Channels page lists the imported channels.
- Every one shows autodownload **off**.
- Names came across. Handles are blank — expected, TubeArchivist does not store
  them.

**4. Verify the baseline, after the first scheduled scan.**

The pending queue must **not** fill with each channel's back catalogue. peeq's
first scan of a channel baselines it, marking existing videos as seen. A
flooded pending queue means baselining did not happen and should be
investigated before Phase B.

**5. Expect the list to shrink.**

Inactive channels were imported deliberately. Over the following days peeq's
auto-unsubscribe will scan them, classify them as `deleted`, and unsubscribe
them. That is working as intended, not data loss.

## TubeArchivist import — Phase B (video archive & media)

Requires a live TubeArchivist with its two volumes mounted read-only into the
peeq container (`--ta-media` = the media volume, `--ta-cache` = the cache
volume where thumbnails live). peeq must be stopped during the import — the
subcommand writes the same SQLite database directly. Use the same API token as
Phase A; resume positions are per-user, so it must be the user whose queue you
are migrating.

**1. Survey — writes and copies nothing.**

```bash
docker compose stop peeq
docker compose run --rm \
  -v ta_media:/ta-media:ro -v ta_cache:/ta-cache:ro \
  peeq import-ta \
  --ta-url http://tubearchivist:8000 --ta-token "$TA_TOKEN" \
  --ta-media /ta-media --ta-cache /ta-cache --dry-run
```

Check:
- "Would import" counts and total size look right for your unwatched queue.
- "Missing files" is 0, or only videos you know TA no longer has on disk.
- The size fits your free space (the real run refuses if it would fill the disk).

**2. Prove it on two channels first — NOT optional.**

```bash
docker compose run --rm -v ta_media:/ta-media:ro -v ta_cache:/ta-cache:ro peeq \
  import-ta --ta-url http://tubearchivist:8000 --ta-token "$TA_TOKEN" \
  --ta-media /ta-media --ta-cache /ta-cache --channels 2
docker compose start peeq
```

In the peeq UI:
- A video plays, its thumbnail renders, and captions display.
- Pick one you were part-way through: the resume position survived, and it did
  **not** flip to watched (it stays in the queue to finish).
- No fully-watched video was imported.
- **Wait for the summarize worker.** A summary, chapters and key points appear,
  and the video is findable via semantic search. If a video that HAS a `.vtt`
  on disk is stuck at "no transcript", the subtitle path convention is wrong —
  stop and fix before the full run.

**3. Full run.**

```bash
docker compose stop peeq
docker compose run --rm -v ta_media:/ta-media:ro -v ta_cache:/ta-cache:ro peeq \
  import-ta --ta-url http://tubearchivist:8000 --ta-token "$TA_TOKEN" \
  --ta-media /ta-media --ta-cache /ta-cache
docker compose start peeq
```

Re-running is safe: already-imported videos are skipped, so no file is
re-copied and no summary is re-billed. Retire TubeArchivist and reclaim its
volumes only after peeq is verified.
