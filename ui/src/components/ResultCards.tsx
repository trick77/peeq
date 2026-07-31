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
  // status is here for one distinction: 'new' means peeq read this video —
  // fetched its captions, summarized and indexed them — but never downloaded
  // it. Search is the only place such a video appears at all.
  status: string;
};

// A search hit can be a video peeq only ever read. It has no file, so its card
// says so instead of offering a play button that leads to a summary page.
function isUnfetched(video: ResultCardVideo): boolean {
  return video.status === "new";
}

export type ResultCardGroup = {
  video: ResultCardVideo;
  matches: SearchMatch[];
};

export function ResultCards({
  results,
  onOpen,
}: {
  results: ResultCardGroup[];
  onOpen: (videoId: string, startSeconds: number) => void;
}) {
  return (
    <>
      {results.map((r) => (
        <div className="result" key={r.video.id}>
          <div className="thumb">
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
            {isUnfetched(r.video) ? null : (
              <div className="play">
                <Icon name="play" size="30px" />
              </div>
            )}
            <span className="dur mono">
              {formatDuration(r.video.duration_seconds)}
            </span>
          </div>
          <div className="rmeta">
            <h3>{r.video.title}</h3>
            <div className="ch">
              {r.video.channel_name || r.video.channel_id}
              {isUnfetched(r.video) ? (
                <span className="badge">Summary only</span>
              ) : null}
            </div>
            <div className="matches">
              {r.matches.map((m, i) => (
                <button
                  key={i}
                  type="button"
                  className="match"
                  onClick={() => onOpen(r.video.id, m.start_seconds)}
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
      {match.kind === "chapter" ? (
        <span className="badge">Chapter</span>
      ) : (
        <span className="src">Transcript</span>
      )}
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
