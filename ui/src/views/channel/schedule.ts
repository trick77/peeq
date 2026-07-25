import type { ChannelDetail, ScanResult } from "../../api/types";

// The channel page reports its scan schedule in two places — the New tab and
// the Settings tab — and they had drifted: New handled a next_scan_at in the
// past ("due now") while Settings printed it raw, so after a manual check
// Settings cheerfully announced a next check that had already happened. One
// module, so the two can never disagree again.
//
// "Check now" does not scan. It pulls next_scan_at into the past and the scan
// loop claims the channel on its next poll, which is why every string here says
// queued and none of them claims a check is running.

// parseSqlUTC turns the backend's SQLite text timestamp ("2026-07-25 06:11:14",
// always UTC, no zone suffix) into a Date. The space-separated form is not ISO
// 8601, so appending "Z" alone leans on engine leniency; swapping the space for
// a "T" first makes it a form every engine parses per spec.
function parseSqlUTC(s: string): Date {
  return new Date(s.replace(" ", "T") + "Z");
}

// isCheckQueued reports whether this channel is waiting to be checked: its next
// scan is due at or before now, so the loop will claim it on its next poll.
// Derived entirely from server state — pressing the button is what makes this
// true, so no client-side "did I just click" flag is needed, and a reload or a
// second browser tab shows the same thing.
export function isCheckQueued(detail: ChannelDetail): boolean {
  if (!detail.next_scan_at) return false;
  return parseSqlUTC(detail.next_scan_at).getTime() <= Date.now();
}

// scheduleLine is the "last checked / next check" sentence. `lead` differs only
// because the New tab's line stands alone while Settings labels a form row.
export function scheduleLine(
  detail: ChannelDetail,
  lead: "Checked" | "Last checked" = "Checked",
): string {
  const parts: string[] = [];
  parts.push(
    detail.last_scanned_at
      ? `${lead} ${parseSqlUTC(detail.last_scanned_at).toLocaleString()}`
      : "Never checked",
  );
  if (detail.next_scan_at) {
    parts.push(
      isCheckQueued(detail)
        ? "check queued"
        : `next check ${parseSqlUTC(detail.next_scan_at).toLocaleString()}`,
    );
  }
  return parts.join(" · ");
}

// scanNotice turns a scan response into the line shown after pressing the
// button. Exported so every "Check now" in the app — the channel page's two
// tabs today, the channels list's row menu next — says the same thing.
export function scanNotice(res: ScanResult): string {
  if (res.status === "blocked") {
    return res.reason ?? "peeq cannot check this channel right now.";
  }
  return "Added to the check queue — usually done within a minute.";
}
