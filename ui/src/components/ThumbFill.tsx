import { useEffect, useState } from "react";
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
// re-renders, and a stale `true` would hide a perfectly good poster.
export function ThumbFill({
  id,
  hasThumbnail,
}: {
  id: string;
  hasThumbnail: boolean;
}) {
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    setFailed(false);
  }, [id]);

  if (!hasThumbnail || failed) {
    return <div className={`fill ${gradientClassFor(id)}`} />;
  }
  return (
    <img
      className="fill"
      src={thumbnailUrl(id)}
      alt=""
      loading="lazy"
      onError={() => setFailed(true)}
    />
  );
}
