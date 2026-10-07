import { memo, useState } from "react";
import { Icon } from "../../icons";
import { seekOnClick } from "../../selection";
import { parseInDepth, readMinutes } from "../../inDepth";

// InDepthBody renders the in-depth summary: the lead, then one headed section
// per key point. Shared by the Player's card and the Inbox reading page, which
// sizes it through its own parent rule. An omitted seek renders the stamps as
// text — there is no video to move on the Inbox page.
export function InDepthBody({
  text,
  seek,
}: {
  text: string;
  seek?: (seconds: number) => void;
}) {
  const doc = parseInDepth(text);
  return (
    <div className="deep">
      {doc.lead.map((p, i) => (
        <p key={i} className="lead">
          {p}
        </p>
      ))}
      {doc.sections.map((s, i) => (
        <section key={i}>
          <h4>
            <span>{s.heading}</span>
            {s.stamp !== null && s.seconds !== null ? (
              seek ? (
                <button
                  type="button"
                  className="ts mono"
                  onClick={seekOnClick(seek, s.seconds)}
                >
                  {s.stamp}
                </button>
              ) : (
                <span className="ts mono">{s.stamp}</span>
              )
            ) : null}
          </h4>
          {s.body.map((p, j) => (
            <p key={j}>{p}</p>
          ))}
        </section>
      ))}
    </div>
  );
}

// InDepthCard is the Player's collapsed card under Highlights: the long reading
// of the video for when the Summary beside it leaves you wanting more. Same
// header-that-opens as Details and the transcript, and it rests shut — most
// visits never want it, and open it is the longest card on the page. The
// Player keys it on the video, so each video starts shut.
function InDepthCardImpl({
  text,
  seek,
}: {
  text: string;
  seek?: (seconds: number) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    /* deeppanel is also the placement hook the single-column order uses. */
    <div className="card deeppanel">
      <button
        type="button"
        className="hd hd-btn"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name="bookOpenText" size="16px" />
        <span className="lbl">In depth</span>
        {!open && (
          <span className="meta glance">{readMinutes(text)} min read</span>
        )}
        <span className="chev">
          <Icon name={open ? "chevronUp" : "chevronDown"} size="15px" />
        </span>
      </button>
      {open && (
        <div className="tabbody">
          <InDepthBody text={text} seek={seek} />
        </div>
      )}
    </div>
  );
}

// Memoised: the Player re-renders on every timeupdate, and neither the text
// nor the stable seek changes with the playhead.
export const InDepthCard = memo(InDepthCardImpl);
