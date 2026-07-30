import { useState } from "react";
import { ThumbFill } from "../../components/ThumbFill";
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
  onQueued,
  onDismissed,
}: {
  video: Video;
  onBack?: () => void;
  // onQueued fires after Download succeeds, so App can seed the queue poll
  // exactly as the Inbox's own button does.
  onQueued?: () => void;
  // onDismissed fires after Ignore, so the caller can leave a page whose
  // subject no longer exists — the row and its summary are deleted server-side.
  onDismissed?: () => void;
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
        onDismissed?.();
      }
      setDecided(kind);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Something went wrong");
      setBusy(false);
    }
  }

  const summary = (video.summary ?? "").trim();
  // The summarizer separates paragraphs with a blank line, the same shape the
  // Player's summary card splits on.
  const paragraphs = summary ? summary.split(/\n\n+/) : [];

  return (
    <div className="unfetched">
      {onBack ? (
        <button type="button" className="unfetched-back" onClick={onBack}>
          &larr; Back to inbox
        </button>
      ) : null}

      <div className="unfetched-head">
        {/* A reference image, not a stage: 260px, and it stacks above the text
          on a narrow screen. Sizing it like a player would promise a play
          button that cannot exist yet. */}
        <div className="thumb unfetched-thumb">
          <ThumbFill
            id={video.id}
            hasThumbnail={true}
            src={pendingThumbnailUrl(video.id)}
          />
          {(video.duration_seconds ?? 0) > 0 ? (
            <span className="dur">
              {formatDuration(video.duration_seconds ?? 0)}
            </span>
          ) : null}
        </div>

        <div className="unfetched-ident">
          <div className="by">
            <span className="chan-name">
              {video.channel_name || video.channel_id}
            </span>
            {video.published_at ? (
              <>
                <span className="dot">·</span>
                {formatAgo(video.published_at)}
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
              {/* Worth saying plainly, because the opposite is the intuitive
                assumption: downloading does not start the analysis over. The
                summary below is the one the library keeps. */}
              <p className="unfetched-note">
                Downloading keeps this summary — it won&rsquo;t be written
                again.
              </p>
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
          <p className="unfetched-empty">
            No speech in this video, so there is nothing to summarize. The title
            and the channel are all peeq knows about it.
          </p>
        ) : video.summary_status === "error" ? (
          <p className="unfetched-empty">
            Reading this video failed. It will be tried again.
          </p>
        ) : (
          <p className="unfetched-empty">
            <Spinner size="15px" />
            Reading this video&rsquo;s captions.
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
        nothing. */}
      {video.has_subtitles ? (
        <div className="unfetched-transcript">
          <TranscriptCard
            vttUrl={subtitlesUrl(video.id)}
            filenameBase={transcriptFilenameBase(video.title)}
          />
        </div>
      ) : null}
    </div>
  );
}
