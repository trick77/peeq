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
}: {
  id: string;
  hasThumbnail: boolean;
}) {
  const [failedId, setFailedId] = useState<string | null>(null);

  if (!hasThumbnail || failedId === id) {
    return <div className={`fill ${gradientClassFor(id)}`} />;
  }
  return (
    <img
      className="fill"
      src={thumbnailUrl(id)}
      alt=""
      loading="lazy"
      onError={() => setFailedId(id)}
    />
  );
}
