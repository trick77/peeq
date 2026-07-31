import { describe, it, expect } from "vitest";
import { isScanQueued, scheduleLine, scanNotice } from "./schedule";
import type { ChannelDetail, ScanResult } from "../../api/types";

// The backend's SQLite text form: UTC, space-separated, no zone suffix.
function sqlUTC(offsetMs: number): string {
  return new Date(Date.now() + offsetMs)
    .toISOString()
    .slice(0, 19)
    .replace("T", " ");
}

function detail(overrides: Partial<ChannelDetail> = {}): ChannelDetail {
  return {
    id: "UCa",
    name: "Uncanny Expeditions",
    handle: "@UncannyExpeditions",
    description: "",
    has_avatar: false,
    has_banner: false,
    verified: false,
    resolved_at: "2026-07-21 06:00:00",
    resolve_ok: true,
    gone: false,
    added: true,
    archived_count: 0,
    runtime_seconds: 0,
    disk_bytes: 0,
    subscribed: true,
    auto_summary: true,
    keep_reads: false,
    autodownload: false,
    format_override: "",
    // Relative to now, not hardcoded: isScanQueued compares next_scan_at to the
    // wall clock, so a fixed future date would silently become "queued" once
    // that date passed and quietly change what these assertions test.
    last_scanned_at: sqlUTC(-18 * 3600 * 1000),
    next_scan_at: sqlUTC(6 * 3600 * 1000),
    pending_count: 0,
    ...overrides,
  };
}

// One unit of work, one name. The backend calls it a "channel scan", Up next
// shows "Channel scan" and the channel header shows "Last channel scan"; this
// module used to be the odd one out with "Checked" / "Never checked" / "next
// check" / "Check now", so the same event wore two names depending on which
// surface you were looking at. That is the same species of confusion as the
// old single "Refreshed" stamp that hid the scan date behind the metadata one.
//
// These assertions are deliberately about the WORD, not just the shape: a
// future edit that reintroduces "check" here fails loudly rather than quietly
// re-splitting the vocabulary.
describe("scan schedule wording", () => {
  it("says scanned, never checked, for a channel with a history", () => {
    const line = scheduleLine(detail());
    expect(line).toMatch(/^Scanned /);
    expect(line).toMatch(/next scan /);
    expect(line).not.toMatch(/check/i);
  });

  it("says Never scanned when the channel has never been scanned", () => {
    const line = scheduleLine(detail({ last_scanned_at: undefined }));
    expect(line).toMatch(/^Never scanned/);
    expect(line).not.toMatch(/check/i);
  });

  it("uses the Settings tab's lead without changing the vocabulary", () => {
    const line = scheduleLine(detail(), "Last scanned");
    expect(line).toMatch(/^Last scanned /);
    expect(line).not.toMatch(/check/i);
  });

  // An overdue next_scan_at means the loop will claim the channel on its next
  // poll — queued, not running, which is what the wording has to convey.
  it("reports a due scan as queued rather than as a date", () => {
    const d = detail({ next_scan_at: sqlUTC(-60_000) });
    expect(isScanQueued(d)).toBe(true);
    const line = scheduleLine(d);
    expect(line).toMatch(/scan queued/);
    expect(line).not.toMatch(/check/i);
  });

  it("omits the second clause when there is no scan schedule at all", () => {
    const line = scheduleLine(
      detail({ last_scanned_at: undefined, next_scan_at: undefined }),
    );
    expect(line).toBe("Never scanned");
  });

  // scanNotice is the one place the post-button wording lives, so the channel
  // page's two tabs and the channels list's row menu cannot drift apart.
  it("words both scan outcomes as a scan, never a check", () => {
    const scheduled: ScanResult = { status: "scheduled" };
    expect(scanNotice(scheduled)).toMatch(/scan queue/);
    expect(scanNotice(scheduled)).not.toMatch(/check/i);

    const blocked: ScanResult = { status: "blocked" };
    expect(scanNotice(blocked)).toMatch(/cannot scan this channel/);
    expect(scanNotice(blocked)).not.toMatch(/check/i);
  });

  // A blocked response carries the backend's own reason when it has one; the
  // fallback above is only for a blocked status with no explanation.
  it("prefers the backend's reason over the generic blocked line", () => {
    const blocked: ScanResult = {
      status: "blocked",
      reason: "YouTube access is paused: rotating cookie",
    };
    expect(scanNotice(blocked)).toBe(
      "YouTube access is paused: rotating cookie",
    );
  });
});
