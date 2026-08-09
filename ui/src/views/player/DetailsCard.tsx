import { Icon } from "../../icons";
import type { Video, VideoEmbeddings } from "../../api/types";
import {
  bitrateLabel,
  codecLabel,
  formatAgo,
  formatDuration,
  formatSize,
  languageLabel,
  resolutionLabel,
} from "../../format";

// KIND_LABELS names each chunk kind the pipeline writes (rag/build.go's
// KindTranscript/KindSummary/KindChapter). A kind with no entry here is shown
// under its own wire word rather than dropped: a row saying "keyframe 12" is
// odd-looking but true, and silently omitting it would make the rows stop
// adding up to the group's other counts.
const KIND_LABELS: Record<string, string> = {
  transcript: "Transcript",
  chapter: "Chapters",
  summary: "Summary",
};

function kindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind;
}

// Thousands separators, pinned to en-US rather than left to toLocaleString's
// default: the page is English throughout and every other figure in it groups
// this way, so a browser set to de-CH must not render this one number as
// 38’412 while the sizes beside it stay 1.4 GB.
const GROUPED = new Intl.NumberFormat("en-US");

// How many of YouTube's own tags are drawn before the rest become a count. Tag
// lists run to thirty or more on a well-optimised channel, and this panel is
// not where anyone reads all of them — the first few say what kind of video
// this is, which is the whole reason they are here.
const TAGS_SHOWN = 8;

type Row = { k: string; v: string; sub?: string };

// Group is one titled block of rows. Rows with no value are dropped, and a
// group left with no rows is dropped too: 109 of the 174 videos in a real
// library predate the migration that added YouTube's own metadata (0009,
// nothing backfills), and a bare "SOURCE" heading over a single line is worse
// than no heading at all.
type Group = { title: string; rows: Row[]; tags?: string[] };

// buildGroups turns the video — and, once it has been fetched, its index
// stats — into the panel's contents. Split out from the component so the
// drop-what-is-missing rule is one readable pass rather than a thicket of JSX
// conditionals.
//
// Note what is NOT here: format_used, the resolved yt-dlp -f selector. It had
// a pill of its own in the player once, and rendered as
// "bestvideo[height<=1080][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4". That
// string describes the request, not the result, and is identical for every
// video downloaded under one preset. Everything under File comes from ffprobe
// reading the actual file instead.
//
// Also deliberately absent: availability ("On YouTube: Available") and
// live_status/media_type ("Kind: Was live"). Both were mocked up and both were
// cut against real data — availability only ever holds 'available' or
// 'unknown' in practice, is written once at metadata time and never
// refreshed, so the row would still read "Available" for a video YouTube
// deleted a year ago.
function buildGroups(video: Video, stats: VideoEmbeddings | null): Group[] {
  const file: Row[] = [
    // formatDuration renders "--:--" for an unknown length, which is the right
    // answer under a scrubber and the wrong one here: a labelled figure
    // reading "--:--" claims the number exists and is unreadable. Guard so the
    // row drops out instead.
    {
      k: "Length",
      v: video.duration_seconds ? formatDuration(video.duration_seconds) : "",
    },
    { k: "Size", v: formatSize(video.filesize_bytes) },
    { k: "Container", v: video.media_container?.toUpperCase() ?? "" },
    {
      k: "Video",
      // One row, because "1080p H.264" is how a person says it — two separate
      // rows would imply the resolution and the codec are independently
      // interesting, and they are not.
      v: [resolutionLabel(video.video_height), codecLabel(video.video_codec)]
        .filter(Boolean)
        .join(" "),
    },
    { k: "Audio", v: codecLabel(video.audio_codec) },
    {
      k: "Average bitrate",
      v: bitrateLabel(video.filesize_bytes, video.duration_seconds),
    },
    { k: "Audio language", v: languageLabel(video.audio_language) },
    // Yes/No, and NOT the audio language: has_subtitles says a subtitle track
    // was stored, and nothing on the wire says what language it is in.
    // Labelling audio_language "Subtitles" would state a fact peeq does not
    // have. Always rendered — a bool is never missing, and "No" is the answer
    // to why the captions toggle is absent from the action row above.
    { k: "Subtitles", v: video.has_subtitles ? "Yes" : "No" },
  ];

  const source: Row[] = [
    // When it entered the archive. Deliberately not "aired", which is on the
    // byline above the title: this panel is about the copy peeq holds.
    {
      k: "Added",
      v: video.downloaded_at ? formatAgo(video.downloaded_at) : "",
    },
    // watched_at is on the wire and rendered nowhere else in peeq — the
    // library card uses it only to count down retention. The row drops for an
    // unwatched video rather than reading "Never": the action row already says
    // so, with a control that changes it.
    { k: "Watched", v: video.watched_at ? formatAgo(video.watched_at) : "" },
    // YouTube's own label, which is a different thing from video.category
    // (peeq's classification, shown as the pill on the byline). Joined rather
    // than truncated: the list is one entry in practice.
    { k: "YouTube category", v: (video.yt_categories ?? []).join(", ") },
    {
      k: "SponsorBlock",
      v: video.sponsorblock_segments?.length
        ? `${video.sponsorblock_segments.length} ${
            video.sponsorblock_segments.length === 1 ? "segment" : "segments"
          }`
        : "",
    },
    // videos.id IS the YouTube id (format.ts's watchURL leans on the same
    // fact), so the panel can report it without a stored URL. It is here
    // because this is the one place in peeq anyone would go looking for it.
    { k: "Video ID", v: video.id },
  ];

  const index: Row[] = [];
  if (stats && stats.chunks > 0) {
    for (const k of stats.kinds) {
      index.push({ k: kindLabel(k.kind), v: String(k.count) });
    }
    // "Embedded", not "text indexed": a chapter chunk is the chapter's own
    // transcript span with its title prefixed, so a chaptered video embeds
    // most of its transcript twice. The figure is the size of the index, which
    // is what it says — reading it as how much video there is would make a
    // chaptered video look twice as talkative as an identical one without
    // chapters.
    index.push({ k: "Tokens embedded", v: GROUPED.format(stats.tokens) });
    if (stats.model) {
      // The model wrote the vectors that are stored, so it is reported even
      // when the server has since been pointed at another one — which is
      // exactly when knowing this is worth anything.
      index.push({
        k: "Model",
        v: stats.model,
        sub: stats.dimensions ? `${stats.dimensions} dimensions` : undefined,
      });
    }
  }

  const groups: Group[] = [
    { title: "File", rows: file },
    {
      title: "Source",
      rows: source,
      tags: (video.yt_tags ?? []).filter(Boolean),
    },
    { title: "Search index", rows: index },
  ];
  return groups
    .map((g) => ({ ...g, rows: g.rows.filter((r) => r.v !== "") }))
    .filter((g) => g.rows.length > 0 || (g.tags?.length ?? 0) > 0);
}

// glanceOf is the collapsed line's right-hand summary — the two or three
// figures worth reading without opening anything, and as many as fit beside
// the label at the narrowest width the line is drawn at. Built from the same
// values as the File group and dropped the same way, so an unprobed video
// shows "34:12 · 412 MB" rather than a row of blanks and separators.
function glanceOf(video: Video): string {
  return [
    video.duration_seconds ? formatDuration(video.duration_seconds) : "",
    formatSize(video.filesize_bytes),
    resolutionLabel(video.video_height),
  ]
    .filter(Boolean)
    .join(" · ");
}

// DetailsCard is the video's technical record: what the file is, where it came
// from, and what search holds for it. Everything in it is reference material —
// true, occasionally load-bearing, and not what anyone opened the page to read
// — so it rests as one labelled line under the action row and opens in place.
//
// It replaced two surfaces. A five-column stat strip
// (Length/Size/Format/Video/Audio) stood here and spent that space on every
// page load; its figures are the File group. A "Search index" card held a slot
// in the rail beside the video; its figures are the Search index group, and
// the rail is now the Summary alone.
//
// Presentational. The Player owns `open` — it persists the choice across
// videos and fetches the index stats the first time the panel is opened, so a
// failed request renders the panel one group short rather than an apology
// under the video.
export function DetailsCard({
  video,
  stats,
  open,
  onToggle,
}: {
  video: Video;
  stats: VideoEmbeddings | null;
  open: boolean;
  onToggle: () => void;
}) {
  const groups = buildGroups(video, stats);
  const glance = glanceOf(video);
  return (
    <div className="details">
      <button
        type="button"
        className="specline"
        aria-expanded={open}
        onClick={onToggle}
      >
        <Icon name={open ? "chevronDown" : "chevronRight"} size="15px" />
        <span className="lbl">Details</span>
        {/* Only while shut. Open, the File group repeats these three figures
            a line below, and a summary of what is already on screen is noise
            — on a video whose size and height are unknown it is the same
            string twice, one above the other. */}
        {!open && glance && <span className="glance">{glance}</span>}
      </button>
      {open && (
        <div className="specbody">
          {groups.length === 0 ? (
            /* Unreachable in practice — the Video ID row alone keeps Source
               populated — but an opened panel must never be an empty box. */
            <p className="placeholder">Nothing to report yet.</p>
          ) : (
            <div className="specgrid">
              {groups.map((g) => (
                <div className="grp" key={g.title}>
                  <h4>{g.title}</h4>
                  <dl>
                    {g.rows.map((r) => (
                      <div className="row" key={r.k}>
                        <dt>{r.k}</dt>
                        <dd>
                          {r.v}
                          {r.sub && <span className="sub">{r.sub}</span>}
                        </dd>
                      </div>
                    ))}
                  </dl>
                  {!!g.tags?.length && (
                    <div className="tags">
                      {g.tags.slice(0, TAGS_SHOWN).map((t) => (
                        <span className="pill" key={t}>
                          {t}
                        </span>
                      ))}
                      {g.tags.length > TAGS_SHOWN && (
                        <span className="pill">
                          +{g.tags.length - TAGS_SHOWN}
                        </span>
                      )}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
