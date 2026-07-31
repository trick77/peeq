import { useState } from "react";
import { thumbnailUrl } from "../api/videos";
import { gradientClassFor } from "../format";

// ThumbFill draws the image that fills a `.thumb` box, falling back to the
// deterministic gradient placeholder when there is no poster to show.
//
// The fallback runs on two conditions, not one. has_thumbnail is a database
// fact (thumbnail_path is set), and the database can outlive the file: a
// tombstone used to unlink the thumbnail while leaving the column set, and a
// file can always vanish from under the media dir by other means. Without an
// onError the browser renders its own broken-image glyph in the middle of an
// otherwise finished-looking card, so a 404 falls back to the same gradient a
// never-had-a-thumbnail video gets.
//
// The failure flag is keyed on the id: these cards are recycled across list
// re-renders, and a stale `true` would hide a perfectly good poster. It stores
// the id that failed rather than a boolean reset by an effect, so the retry for
// a new id happens in the same render the id changes in — an effect-based reset
// would paint the placeholder for one commit first, and would make the retry
// depend on passive-effect flush timing.
export function ThumbFill({
  id,
  hasThumbnail,
  version,
  src,
}: {
  id: string;
  hasThumbnail: boolean;
  // version is the row's thumbnail_version. It goes in the URL so the poster
  // can be cached immutably — the URL changes exactly when the bytes do. The
  // prop lives here rather than at each caller so every card builds the same
  // URL and they all share one cache entry per poster. Ignored when src is
  // given: the Inbox versions its own endpoint's URL.
  version?: string;
  // src overrides the default downloaded-video thumbnail endpoint. The Inbox
  // passes the pending-thumbnail endpoint here: a pending item has no videos
  // row, so thumbnailUrl(id) (which points at /api/videos/{id}/thumbnail)
  // wouldn't resolve. Everything else — the id-keyed failure flag, the onError
  // fallback to the gradient — is shared, so a broken poster degrades the same
  // way on both.
  src?: string;
}) {
  const [failedId, setFailedId] = useState<string | null>(null);

  if (!hasThumbnail || failedId === id) {
    return <div className={`fill ${gradientClassFor(id)}`} />;
  }
  return (
    <img
      className="fill"
      src={src ?? thumbnailUrl(id, version)}
      alt=""
      loading="lazy"
      onError={() => setFailedId(id)}
    />
  );
}
