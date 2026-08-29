import { useState } from "react";
import { ThumbFill } from "../../components/ThumbFill";
import { ChannelLink } from "../../components/ChannelLink";
import { Icon } from "../../icons";
import { downloadPending, ignorePending } from "../../api/pending";
import { pendingThumbnailUrl } from "../../api/videos";
import { subtitlesUrl } from "../../api/search";
import { TranscriptCard } from "../../components/TranscriptCard";
import { transcriptFilenameBase } from "../../vtt";
import type { Video } from "../../api/types";
import { formatAgo, formatDuration } from "../../format";
import { Button, Spinner } from "../../ui";

// UnfetchedVideo is /video/<id> for a video peeq has read but not downloaded:
// one it fetched captions for and summarized so the user could judge it before
// spending the disk and the wait.
//
// It shares the route with the Player rather than having one of its own, and
// that is the point of the design. The URL a video has in the Inbox is the URL
// it keeps after downloading — nothing to redirect, no link that goes stale,
// and pressing Download here does not navigate anywhere.
//
// What it is NOT: a player with the video missing. There is no stage, no
// scrubber, no SponsorBlock, no resume, no category picker, no share control.
// The summary is the entire content of the page, so it is set for reading —
// body-size type on a bounded measure — and the thumbnail is a reference image
// beside the title rather than a 16:9 slab standing in for a video that is not
// there.
export function UnfetchedVideo({
  video,
  onBack,
  backLabel = "Back to inbox",
  onQueued,
  onDismissed,
  inboxOrder,
  onOpenInboxVideo,
  onOpenChannel,
}: {
  video: Video;
  // onBack is absent when this page was not reached from anywhere that offers a
  // way back — opened from the Library, from a channel tab, or from a cold
  // link. It used to be passed unconditionally, which is how a video reached
  // from the Library ended up offering "Back to inbox" to a reader who had
  // never been there.
  onBack?: () => void;
  // What that place is called. The page is reached from the Inbox and from
  // Search now, and naming the wrong one is worse than naming none at all.
  backLabel?: string;
  // onQueued fires after Download succeeds, so App can seed the queue poll
  // exactly as the Inbox's own button does.
  onQueued?: () => void;
  // onDismissed fires after Ignore, so the caller can leave a page whose
  // subject no longer exists — the row and its summary are deleted server-side.
  onDismissed?: () => void;
  // inboxOrder is the ids the Inbox is currently showing, in on-screen order.
  // It comes from the Inbox rather than being fetched here because that order
  // is the product of a search box, a channel chip and a sort select — a page
  // that re-derived it would say "3 of 40" while the grid behind it showed six.
  //
  // Empty until the Inbox has been opened at least once, which is the honest
  // answer for a cold deep-link: there is no inbox position to be at, so there
  // is no stepper.
  inboxOrder?: string[];
  onOpenInboxVideo?: (id: string) => void;
  // onOpenChannel is optional for the same reason it is on MetaHeader: this
  // page is reachable from places that have nowhere to navigate to, and there
  // the channel renders as plain text rather than a dead link.
  onOpenChannel?: (channelId: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [decided, setDecided] = useState<"queued" | "ignored" | null>(null);

  async function decide(kind: "queued" | "ignored") {
    setBusy(true);
    setError(null);
    try {
      if (kind === "queued") {
        await downloadPending(video.id);
        onQueued?.();
      } else {
        await ignorePending(video.id);
      }
      // Deciding moves you on. That is the whole point of opening these pages
      // one after another, and it is why Ignore does not simply call
      // onDismissed: going back to the grid after every decision is exactly the
      // round trip the stepper exists to avoid.
      //
      // The next id is taken from the order as it was when the page opened.
      // That list is now one item stale — this video is still in it — but the
      // item AFTER it is unaffected, which is all that is being read.
      if (nextID && onOpenInboxVideo) {
        onOpenInboxVideo(nextID);
        return;
      }
      // Nothing left to move to: an ignore has nothing to show, so leave.
      if (kind === "ignored") {
        onDismissed?.();
        return;
      }
      setDecided(kind);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Something went wrong");
      setBusy(false);
    }
  }

  // Where this video sits in the inbox, and what is on either side of it.
  // -1 means it is not in the list at all — opened from the Library, from a
  // link, or after the Inbox moved on — and everything below then renders
  // nothing rather than guessing a position.
  const order = inboxOrder ?? [];
  const at = order.indexOf(video.id);
  const prevID = at > 0 ? order[at - 1] : null;
  const nextID = at >= 0 && at < order.length - 1 ? order[at + 1] : null;
  const canStep = at >= 0 && order.length > 1 && !!onOpenInboxVideo;

  const summary = (video.summary ?? "").trim();
  // The summarizer separates paragraphs with a blank line, the same shape the
  // Player's summary card splits on.
  const paragraphs = summary ? summary.split(/\n\n+/) : [];

  return (
    <div className="unfetched">
      <div className="unfetched-nav">
        {onBack ? (
          <button type="button" className="unfetched-back" onClick={onBack}>
            &larr; {backLabel}
          </button>
        ) : null}

        {/* The stepper is what makes reading a backlog bearable: open, read,
          decide, next, without returning to the grid between each one. It
          appears only when there is genuinely somewhere to step — a video
          opened from the Library or a link has no inbox position, and a
          disabled pair of arrows on those pages would be furniture. */}
        {canStep ? (
          <div className="unfetched-step">
            <span className="unfetched-count mono">
              {at + 1} of {order.length}
            </span>
            <button
              type="button"
              className="ui-btn ui-btn--ghost"
              disabled={!prevID}
              onClick={() => prevID && onOpenInboxVideo?.(prevID)}
            >
              &larr; Prev
            </button>
            <button
              type="button"
              className="ui-btn ui-btn--ghost"
              disabled={!nextID}
              onClick={() => nextID && onOpenInboxVideo?.(nextID)}
            >
              Next &rarr;
            </button>
          </div>
        ) : null}
      </div>

      <div className="unfetched-head">
        {/* A reference image, not a stage: 260px, and it stacks above the text
          on a narrow screen. Sizing it like a player would promise a play
          button that cannot exist yet. */}
        <div className="thumb unfetched-thumb">
          <ThumbFill
            id={video.id}
            hasThumbnail={true}
            src={pendingThumbnailUrl(video.id, video.thumbnail_version)}
          />
          {(video.duration_seconds ?? 0) > 0 ? (
            <span className="dur">
              {formatDuration(video.duration_seconds ?? 0)}
            </span>
          ) : null}
        </div>

        <div className="unfetched-ident">
          {/* The same eyebrow the library card and the Player carry, down to
              the markup — see MetaHeader, and .playmeta/.unfetched-ident .by in
              index.css for the rule the two share. Only "aired" appears here:
              there is no downloaded_at to report, which is the whole point of
              the page. It stays conditional because published_at is unknown for
              some live streams and premieres. */}
          <div className="by">
            <ChannelLink
              channelId={video.channel_id}
              name={video.channel_name}
              onOpenChannel={onOpenChannel}
            />
            {video.published_at ? (
              <>
                <span className="dot">·</span>
                aired {formatAgo(video.published_at)}
              </>
            ) : null}
          </div>
          <h1>{video.title}</h1>

          {decided ? (
            <p className="unfetched-decided">
              {decided === "queued"
                ? "Queued for download."
                : "Removed from your inbox."}
            </p>
          ) : (
            <>
              <div className="unfetched-acts">
                <Button
                  type="button"
                  variant="primary"
                  disabled={busy}
                  onClick={() => decide("queued")}
                >
                  <Icon name="download" size="15px" />
                  Download
                </Button>
                <Button
                  type="button"
                  variant="dangerQuiet"
                  disabled={busy}
                  onClick={() => decide("ignored")}
                >
                  <Icon name="trash" size="15px" />
                  Ignore
                </Button>
              </div>
            </>
          )}
          {error ? <div className="errline">{error}</div> : null}
        </div>
      </div>

      <div className="unfetched-body">
        {paragraphs.length > 0 ? (
          paragraphs.map((p, i) => <p key={i}>{p}</p>)
        ) : video.summary_status === "no_transcript" ? (
          // Deliberately not phrased as captions: the same status covers "there
          // are none" and "they turned out to be music", which is the wording
          // the Player already settled on.
          //
          // What it does split on is has_subtitles, because "the title and the
          // channel are all peeq knows about it" is flatly false when a
          // transcript panel is rendering underneath it. The Inbox used to send
          // you here on a "Read transcript" button and no longer offers that
          // card at all, but this page is still reached for such a video from
          // the Library and the channel tabs, so the split stays. Still no
          // cause named: captions with no summary also cover an empty file and
          // one that could not be read, and neither is music.
          video.has_subtitles ? (
            <p className="unfetched-empty">
              No speech peeq could summarize in this video. The captions it
              fetched are below.
            </p>
          ) : (
            <p className="unfetched-empty">
              No speech in this video, so there is nothing to summarize. The
              title and the channel are all peeq knows about it.
            </p>
          )
        ) : video.summary_status === "error" ? (
          <p className="unfetched-empty">
            Summarizing this video failed. It will be tried again.
          </p>
        ) : !video.has_subtitles ? (
          // No transcript means no summary job: peeq is waiting on YouTube to
          // publish captions, which it chases for roughly 31 hours before
          // settling the video as no_transcript. No spinner — nothing is
          // running, and the next attempt can be a day away.
          <p className="unfetched-empty">
            Waiting for YouTube to publish captions for this video.
          </p>
        ) : (
          <p className="unfetched-empty">
            <Spinner size="15px" />
            Summarizing this video.
          </p>
        )}
      </div>

      {/* The captions are already on disk — reading them is what produced the
        summary above — and parsing them is client-side, so the panel costs a
        fetch of a file peeq already has and no model call at all.
        
        It earns its place: skimming for one term is often how you settle a
        maybe that 190 words left open, and the .txt / .vtt / Copy row means you
        can take the text away without downloading the video.

        No seek prop. There is no media to jump to yet, so the cue rows render
        as text rather than as buttons that would look identical and do
        nothing.

        Expanded for a no_transcript video, and only for that one: there is no
        summary coming, so the transcript is the entire content of the page.
        (It was also where the Inbox's "Read transcript" button landed, until
        the Inbox stopped offering those cards at all — arriving from the
        Library or a channel tab, the reason is the same.)
        A video still being summarized keeps the panel folded — the summary is
        what that page is waiting to show. Keyed on the video for the same reason the Player
        keys it: the stepper walks from one inbox video to the next without
        unmounting this page, and without the key you would land on the next
        one with the panel still open and the previous video's find term still
        in the box. */}
      {video.has_subtitles ? (
        <div className="unfetched-transcript">
          <TranscriptCard
            key={video.id}
            vttUrl={subtitlesUrl(video.id)}
            filenameBase={transcriptFilenameBase(video.title)}
            defaultOpen={video.summary_status === "no_transcript"}
          />
        </div>
      ) : null}
    </div>
  );
}
