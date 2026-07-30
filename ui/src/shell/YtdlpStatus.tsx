// YtdlpStatus — the rail-foot indicator for a waiting yt-dlp update, sitting
// directly under CookieStatus and sharing its styling.
//
// Unlike the cookie row it renders only when there is something to say. The
// cookie is a standing precondition worth confirming ("still signed in"); a
// yt-dlp version is not something the user needs reassuring about every page
// load, and a permanently-green second row would just dilute the first one.
// The caller is what decides: it passes nothing when no update is waiting.
//
// It is deliberately not a link or a button. Installing is Settings' Update
// button and nothing else — a mutating action that replaces the binary every
// download depends on does not belong in nav chrome, one stray click away.
export function YtdlpStatus({
  version,
  latest,
}: {
  version: string;
  latest: string;
}) {
  return (
    <div className="ytdlp-status warn">
      <span className="led" aria-hidden="true" />
      <div>
        yt-dlp <b>update available</b>
        <br />
        <span className="ytdlp-detail">
          {version} → {latest}
        </span>
      </div>
    </div>
  );
}
