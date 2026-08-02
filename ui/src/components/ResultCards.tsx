import { Icon } from "../icons";
import { ThumbFill } from "./ThumbFill";
import { pendingThumbnailUrl } from "../api/videos";
import { formatDuration } from "../format";
import { splitHighlights } from "../highlight";
import type { SearchMatch } from "../api/search";

// ResultCards renders a list of videos and the moments matched within them.
//
// Both search modes render through it, from different sources: Find gets the
// grouped response of /api/search, Ask derives its list from the moments the
// answer cited. ResultCardVideo is the intersection of what those two carry —
// api/types Video satisfies it structurally, so neither caller needs a cast.
// Anything added here has to be added to answerVideo as well, which is what
// status cost: the Ask stream is deliberately narrow and did not carry one.
export type ResultCardVideo = {
  id: string;
  title: string;
  channel_id: string;
  channel_name: string;
  // Optional because Video's is: the backend omits a duration it never learned,
  // and formatDuration renders that as a dash.
  duration_seconds?: number;
  has_thumbnail: boolean;
  thumbnail_version?: string;
  // status is here for the two ways a search hit can have no file to play.
  // 'new' means peeq read this video — fetched its captions, summarized and
  // indexed them — but never downloaded it; Search is the only place such a
  // video appears at all. 'tombstoned' means it HAD a file and the sweep
  // reclaimed the space, keeping everything else.
  status: string;
};

// A search hit can be a video peeq only ever read. It has no file, so its card
// says so instead of offering a play button that leads to a summary page.
function isUnfetched(video: ResultCardVideo): boolean {
  return video.status === "new";
}

// A tombstoned video is the other kind of fileless hit: downloaded once, then
// swept to reclaim the disk. Everything except the media survives — transcript,
// summary, poster — which is exactly why it is still findable here.
//
// Kept apart from isUnfetched rather than folded into one "has no file" test,
// because the two are different situations and lead to different pages. One was
// never fetched and offers Download; this one offers Re-download, and its page
// says so. Telling a reader "Not downloaded" about a video they watched last
// month would be the same class of wrong as the "Summary only" pill that sat
// above a transcript.
function isReclaimed(video: ResultCardVideo): boolean {
  return video.status === "tombstoned";
}

// Whether the card may promise playback. Neither kind has a file behind it, so
// neither gets the play triangle that says there is one.
function hasNoFile(video: ResultCardVideo): boolean {
  return isUnfetched(video) || isReclaimed(video);
}

export type ResultCardGroup = {
  video: ResultCardVideo;
  matches: SearchMatch[];
};

// summaryFirst hoists the summary moment to the top of a card's list.
//
// The summary describes the whole video, so it belongs above the moments rather
// than wherever its retrieval score happened to land it: a card whose middle row
// says "this video is about X" between moments at 4:12 and 11:38 reads as three
// unrelated findings, with timestamps running backwards down the page.
//
// Deliberately NOT done in the sources the cards come from. Find's order is bm25
// rank and Ask's is citation order (answerSources.groupCited), and Ask's is
// load-bearing elsewhere: AnswerPanel's numbered source list ascends down the
// page in exactly that order. The cards render no numbers, so only they reorder.
//
// The backend allows at most one summary hit per video (`admits`, search
// handlers), so this is a single hoist and everything else keeps its order.
function summaryFirst(matches: SearchMatch[]): SearchMatch[] {
  if (matches.length < 2) return matches;
  const at = matches.findIndex((m) => m.kind === "summary");
  if (at <= 0) return matches;
  return [matches[at], ...matches.slice(0, at), ...matches.slice(at + 1)];
}

export function ResultCards({
  results,
  onOpen,
  onOpenVideo,
  onOpenChannel,
}: {
  results: ResultCardGroup[];
  // onOpen jumps into a video AT a moment. onOpenVideo opens it with no seek at
  // all, which is not the same as onOpen(id, 0): Player applies any seekTo that
  // is not undefined, so a 0 would rewind the playhead to the start and the next
  // resume flush would write that 0 over a half-watched video's stored position.
  // The title, the thumbnail and a summary match all open the video where the
  // viewer left it.
  onOpen: (videoId: string, startSeconds: number) => void;
  onOpenVideo: (videoId: string) => void;
  onOpenChannel: (channelId: string) => void;
}) {
  return (
    <>
      {results.map((r) => (
        <div className="result" key={r.video.id}>
          {/* The thumbnail opens the video, as it does on every other card in
              the app. It was inert here while .result:hover and the play
              overlay both advertised a click that did nothing. */}
          <button
            type="button"
            className="thumb rthumb"
            aria-label={`Open ${r.video.title}`}
            onClick={() => onOpenVideo(r.video.id)}
          >
            {/* An unfetched video's only poster is the one cached for the
                Inbox: it has no videos-row thumbnail, so the default endpoint
                would 404 into the gradient. Same source UnfetchedVideo reads,
                and the onError fallback still covers a poster that has gone. */}
            <ThumbFill
              id={r.video.id}
              hasThumbnail={isUnfetched(r.video) || r.video.has_thumbnail}
              version={r.video.thumbnail_version}
              src={
                isUnfetched(r.video)
                  ? pendingThumbnailUrl(r.video.id, r.video.thumbnail_version)
                  : undefined
              }
            />
            {/* The play triangle says "there is a file behind this". A reclaimed
                video has none either, and its card was still drawing one. */}
            {hasNoFile(r.video) ? null : (
              <div className="play">
                <Icon name="play" size="30px" />
              </div>
            )}
            <span className="dur mono">
              {formatDuration(r.video.duration_seconds)}
            </span>
          </button>
          <div className="rmeta">
            <h3>
              <button
                type="button"
                className="rtitle"
                onClick={() => onOpenVideo(r.video.id)}
              >
                {r.video.title}
              </button>
            </h3>
            <div className="ch">
              {/* Same control the Library, the Player and the channel tabs use
                  for a channel name (.chan-link) — the class is shared, so it is
                  reused rather than restyled here. */}
              {r.video.channel_id ? (
                <button
                  type="button"
                  className="chan-link"
                  onClick={() => onOpenChannel(r.video.channel_id)}
                >
                  {r.video.channel_name || r.video.channel_id}
                </button>
              ) : (
                r.video.channel_name
              )}
              {/* "Not downloaded", not "Summary only": peeq keeps a kept read's
                  transcript indefinitely, so such a card routinely lists
                  transcript moments underneath — which made the old wording
                  contradict the rows directly below it. What this video is
                  missing is the file, and that is what the pill now says.

                  "Reclaimed" is the other fileless case, and it gets its own
                  word rather than sharing this one: that video WAS downloaded,
                  and telling someone it was never downloaded is the same class
                  of wrong the "Summary only" pill was. */}
              {isUnfetched(r.video) ? (
                <span className="badge">Not downloaded</span>
              ) : isReclaimed(r.video) ? (
                <span className="badge">Reclaimed</span>
              ) : null}
            </div>
            <div className="matches">
              {summaryFirst(r.matches).map((m, i) => (
                <button
                  key={i}
                  type="button"
                  className="match"
                  onClick={() =>
                    // A summary chunk is stored at start_seconds 0 — it has no
                    // timestamp of its own — so seeking to it would rewind the
                    // video to the start and then overwrite the stored resume
                    // position with 0. It opens the video instead.
                    m.kind === "summary"
                      ? onOpenVideo(r.video.id)
                      : onOpen(r.video.id, m.start_seconds)
                  }
                >
                  <MatchLead match={m} />
                  <span className="snip">
                    <Snippet text={m.snippet} />
                  </span>
                </button>
              ))}
            </div>
          </div>
        </div>
      ))}
    </>
  );
}

// MatchLead renders the left of a match row: where the hit came from, and when
// it happens. A summary describes the whole video and has no timestamp; a
// chapter and a transcript moment both do.
//
// A transcript moment carries no label at all. It used to say "TRANSCRIPT"
// beside its own timestamp — the one thing a timestamp already makes obvious —
// and repeating it on nearly every row made the two kinds that DO need saying
// harder to spot. Summary and Chapter stay for exactly that reason.
//
// This replaces the raw vector distance that used to sit at the end of the row.
// That number was retrieval diagnostics — it is meaningless for a bm25-ranked
// keyword hit, and it told a user nothing they could act on.
function MatchLead({ match }: { match: SearchMatch }) {
  if (match.kind === "summary") {
    return <span className="badge">Summary</span>;
  }
  return (
    <>
      <span className="ts mono">{formatDuration(match.start_seconds)}</span>
      {match.kind === "chapter" ? <span className="badge">Chapter</span> : null}
    </>
  );
}

// Snippet renders a search preview, marking the terms that matched. The
// backend delimits them with control characters rather than markup precisely so
// this can build text nodes and <mark> elements instead of setting innerHTML.
function Snippet({ text }: { text: string }) {
  const segments = splitHighlights(text);
  return (
    <>
      {segments.map((s, i) =>
        s.match ? <mark key={i}>{s.text}</mark> : <span key={i}>{s.text}</span>,
      )}
    </>
  );
}
