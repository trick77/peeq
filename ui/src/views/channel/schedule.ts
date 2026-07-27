import type { ChannelDetail, ScanResult } from "../../api/types";
import { DOT } from "../../sep";

// The channel page reports its scan schedule in two places — the New tab and
// the Settings tab — and they had drifted: New handled a next_scan_at in the
// past ("due now") while Settings printed it raw, so after a manual scan
// Settings cheerfully announced a next scan that had already happened. One
// module, so the two can never disagree again.
//
// "Scan now" does not scan on the spot. It pulls next_scan_at into the past and
// the scan loop claims the channel on its next poll, which is why every string
// here says queued and none of them claims a scan is running.
//
// Every string here says SCAN, never "check". One unit of work gets one name
// across the whole app: the backend calls it a "channel scan"
// (handleActivityUpcoming), Up next shows "Channel scan", and the channel
// header shows "Last channel scan". This module used to be the odd one out
// with "Checked" / "Never checked" / "next check", which is the same species
// of confusion as the single "Refreshed" stamp that hid the scan date — one
// event wearing several names, so no two surfaces obviously talk about it.

// parseSqlUTC turns the backend's SQLite text timestamp ("2026-07-25 06:11:14",
// always UTC, no zone suffix) into a Date. The space-separated form is not ISO
// 8601, so appending "Z" alone leans on engine leniency; swapping the space for
// a "T" first makes it a form every engine parses per spec.
function parseSqlUTC(s: string): Date {
  return new Date(s.replace(" ", "T") + "Z");
}

// isScanQueued reports whether this channel is waiting to be scanned: its next
// scan is due at or before now, so the loop will claim it on its next poll.
// Derived entirely from server state — pressing the button is what makes this
// true, so no client-side "did I just click" flag is needed, and a reload or a
// second browser tab shows the same thing.
export function isScanQueued(detail: ChannelDetail): boolean {
  if (!detail.next_scan_at) return false;
  return parseSqlUTC(detail.next_scan_at).getTime() <= Date.now();
}

// scheduleLine is the "last scanned / next scan" sentence. `lead` differs only
// because the New tab's line stands alone while Settings labels a form row.
export function scheduleLine(
  detail: ChannelDetail,
  lead: "Scanned" | "Last scanned" = "Scanned",
): string {
  const parts: string[] = [];
  parts.push(
    detail.last_scanned_at
      ? `${lead} ${parseSqlUTC(detail.last_scanned_at).toLocaleString()}`
      : "Never scanned",
  );
  if (detail.next_scan_at) {
    parts.push(
      isScanQueued(detail)
        ? "scan queued"
        : `next scan ${parseSqlUTC(detail.next_scan_at).toLocaleString()}`,
    );
  }
  return parts.join(DOT);
}

// scanNotice turns a scan response into the line shown after pressing the
// button. Exported so every "Scan now" in the app — the channel page's two
// tabs and the channels list's row menu — says the same thing.
export function scanNotice(res: ScanResult): string {
  if (res.status === "blocked") {
    return res.reason ?? "peeq cannot scan this channel right now.";
  }
  return "Added to the scan queue — usually done within a minute.";
}
