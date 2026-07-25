import { type IconName } from "../icons";

// agenda — the vocabulary Up next and History share. Both pages describe the
// same work, one before it happens and one after, so the wording, the icons and
// the time arithmetic have to come from one place: a scan that reads "Scan ·
// Veritasium" while it is scheduled and something else once it is logged would
// make the two pages look like two apps. Split out of the old Activity view,
// which owned all of this when it was the only page rendering an agenda.

// parseUTC reads the backend's "2006-01-02 15:04:05" UTC text into a Date.
export function parseUTC(at: string): Date {
  return new Date(at.replace(" ", "T") + "Z");
}

// relTime renders a compact relative label against now. Coarse on purpose — an
// agenda is about sequence, not exact clock times. Future and past are worded
// separately so a scheduled task never reads as "ago": a scan due in 40 minutes
// is "in 40m", and one whose instant just passed but hasn't been claimed yet is
// "soon", never "1m ago".
export function relTime(date: Date, now: number): string {
  const secs = Math.round((date.getTime() - now) / 1000);
  const a = Math.abs(secs);
  const mag =
    a < 3600
      ? `${Math.max(1, Math.round(a / 60))}m`
      : a < 86400
        ? `${Math.round(a / 3600)}h`
        : `${Math.round(a / 86400)}d`;
  if (secs >= 0) return a < 60 ? "soon" : `in ${mag}`;
  return a < 45 ? "just now" : `${mag} ago`;
}

// plannedWhen labels a FUTURE row. It never says "ago": an item with no instant
// is "up next", one still ahead is "in 40m", and one whose scheduled instant has
// already passed — an overdue task the worker hasn't reached yet (e.g. because
// YouTube is paused) — is "soon", not "1h ago". Only History's log uses relTime's
// "ago" wording.
export function plannedWhen(atStr: string | undefined, now: number): string {
  if (!atStr) return "up next";
  const secs = Math.round((parseUTC(atStr).getTime() - now) / 1000);
  return secs < 60 ? "soon" : relTime(parseUTC(atStr), now);
}

// leadCap uppercases a leading lowercase ASCII letter so every detail line's
// first word reads as a capital, without mangling a number ("3 new") or a term
// that is already cased ("512 MB").
export function leadCap(s: string): string {
  const c = s.charCodeAt(0);
  return c >= 97 && c <= 122 ? s[0].toUpperCase() + s.slice(1) : s;
}

// KIND maps an event/projection kind to its icon and display label. The icon's
// shape carries the kind so the text never has to name it; the label is the
// fallback subject for a kindless event (retention, yt-dlp).
const KIND: Record<string, { icon: IconName; label: string }> = {
  scan: { icon: "search", label: "Scan" },
  channel_meta: { icon: "refresh", label: "Metadata" },
  download: { icon: "download", label: "Download" },
  summary: { icon: "alignLeft", label: "Summary" },
  retention: { icon: "trash", label: "Cleanup" },
  ytdlp: { icon: "settings", label: "yt-dlp" },
  access: { icon: "warning", label: "Access" },
};

export function kindOf(k: string): { icon: IconName; label: string } {
  return KIND[k] ?? { icon: "clock", label: k };
}

// CHANNEL_KINDS are the rows whose subject is a channel, and so the only ones
// whose name links anywhere. A download or summary row names a video, which the
// agenda does not link — it is a record of work, not a video browser.
const CHANNEL_KINDS = new Set(["scan", "channel_meta"]);

// subjectNode renders a row's subject, as a link to the channel page when the
// row is about a channel and we know its id. Both pages use it: a linked name on
// one and dead text on the other would be exactly the kind of inconsistency
// splitting the agenda in two is supposed to avoid.
export function subjectNode(
  kind: string,
  subjectID: string | undefined,
  text: string,
  onOpenChannel?: (id: string) => void,
) {
  if (!subjectID || !onOpenChannel || !CHANNEL_KINDS.has(kind)) return text;
  const id = subjectID;
  return (
    <button type="button" className="ag-link" onClick={() => onOpenChannel(id)}>
      {text}
    </button>
  );
}
