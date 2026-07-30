import { describe, expect, it } from "vitest";
import {
  codecLabel,
  daysSince,
  formatAge,
  formatAgo,
  formatSize,
  resolutionLabel,
  shortWatchLink,
  videoLabel,
  watchURL,
} from "./format";

// A fixed "now" so the relative output is deterministic. daysSince (which
// formatAgo builds on) floors whole days, so each case picks a day offset
// squarely inside its bucket rather than on a boundary.
const NOW = new Date("2026-07-24T12:00:00Z");
const daysAgo = (n: number) =>
  new Date(NOW.getTime() - n * 86400000).toISOString();

describe("formatAgo", () => {
  it("returns empty string for a missing timestamp", () => {
    expect(formatAgo(undefined, NOW)).toBe("");
  });

  it("says 'today' for now and for a future date", () => {
    expect(formatAgo(NOW.toISOString(), NOW)).toBe("today");
    expect(formatAgo(daysAgo(-3), NOW)).toBe("today");
  });

  it("counts days with singular/plural under a month", () => {
    expect(formatAgo(daysAgo(1), NOW)).toBe("1 day ago");
    expect(formatAgo(daysAgo(3), NOW)).toBe("3 days ago");
    expect(formatAgo(daysAgo(29), NOW)).toBe("29 days ago");
  });

  it("rounds to months once past 30 days", () => {
    expect(formatAgo(daysAgo(30), NOW)).toBe("1 month ago");
    expect(formatAgo(daysAgo(150), NOW)).toBe("5 months ago");
    expect(formatAgo(daysAgo(344), NOW)).toBe("11 months ago");
  });

  // Twelve months is a year and has to read as one. Neither "12 months ago"
  // (the arithmetic left unattended) nor "11 months ago" (clamping, which
  // under-reports something 11½ months old) is acceptable here.
  it("rolls into years as soon as the month count would reach 12", () => {
    expect(formatAgo(daysAgo(345), NOW)).toBe("1 year ago");
    expect(formatAgo(daysAgo(364), NOW)).toBe("1 year ago");
    expect(formatAgo(daysAgo(365), NOW)).toBe("1 year ago");
  });

  it("rounds to years beyond the first", () => {
    expect(formatAgo(daysAgo(740), NOW)).toBe("2 years ago");
    expect(formatAgo(daysAgo(1100), NOW)).toBe("3 years ago");
  });

  it("never says '12 months ago' at any age", () => {
    for (let days = 1; days <= 3650; days++) {
      expect(formatAgo(daysAgo(days), NOW)).not.toContain("12 months");
    }
  });
});

// downloaded_at and watched_at arrive as SQLite's datetime('now') — UTC with a
// space and no zone marker, not the 'Z'-suffixed ISO the other cases use. JS
// parses that shape as LOCAL time, so without normalization the age is off by
// the viewer's UTC offset and reads "today" for a video added yesterday.
describe("backend timestamp shape", () => {
  const sqliteStamp = (n: number) =>
    new Date(NOW.getTime() - n * 86400000)
      .toISOString()
      .replace("T", " ")
      .slice(0, 19);

  it("reads a space-separated stamp as UTC, not local time", () => {
    expect(formatAgo(sqliteStamp(1), NOW)).toBe("1 day ago");
    expect(formatAgo(sqliteStamp(3), NOW)).toBe("3 days ago");
    expect(formatAge(sqliteStamp(1), NOW)).toBe("1 d ago");
    expect(daysSince(sqliteStamp(5), NOW)).toBe(5);
  });

  // The day boundary is where a misparse actually shows. Under a positive UTC
  // offset the local reading lands EARLIER than the real instant, inflating
  // the age: 23 hours becomes 25 in Zurich, and "today" becomes "1 day ago".
  it("keeps a stamp under 24h old at 'today' whatever the viewer's zone", () => {
    const justUnderADay = new Date(NOW.getTime() - 23 * 3600000)
      .toISOString()
      .replace("T", " ")
      .slice(0, 19);
    expect(daysSince(justUnderADay, NOW)).toBe(0);
    expect(formatAgo(justUnderADay, NOW)).toBe("today");
  });
});

describe("formatAge", () => {
  // The channel header renders formatAge into a stat cell that must not
  // collapse, and the library card eyebrow puts it beside a formatAgo value.
  // Both depend on the em dash, so pin it: a later pass at making the two
  // helpers "consistent" must not quietly turn it into an empty string.
  it("returns an em dash, not an empty string, for unknown input", () => {
    expect(formatAge(undefined, NOW)).toBe("—");
    expect(formatAge("not-a-date", NOW)).toBe("—");
  });

  it("says 'today' for now and for a future date", () => {
    expect(formatAge(NOW.toISOString(), NOW)).toBe("today");
    expect(formatAge(daysAgo(-3), NOW)).toBe("today");
  });

  it("renders the same buckets as formatAgo, abbreviated", () => {
    expect(formatAge(daysAgo(1), NOW)).toBe("1 d ago");
    expect(formatAge(daysAgo(10), NOW)).toBe("10 d ago");
    expect(formatAge(daysAgo(90), NOW)).toBe("3 mo ago");
    expect(formatAge(daysAgo(400), NOW)).toBe("1 y ago");
  });

  it("rolls into years with formatAgo rather than reading '12 mo ago'", () => {
    expect(formatAge(daysAgo(344), NOW)).toBe("11 mo ago");
    expect(formatAge(daysAgo(345), NOW)).toBe("1 y ago");
    expect(formatAge(daysAgo(364), NOW)).toBe("1 y ago");
  });
});

// The two formatters exist only to spell the same age differently. Anything
// that makes one pick a different bucket than the other is a bug in whichever
// one changed, so assert the agreement directly rather than trusting two lists
// of hand-written cases to stay in step.
describe("formatAgo and formatAge agree on every bucket", () => {
  const EXPECTED_UNIT = [
    { days: 1, long: "day", short: "d" },
    { days: 29, long: "day", short: "d" },
    { days: 30, long: "month", short: "mo" },
    { days: 200, long: "month", short: "mo" },
    { days: 344, long: "month", short: "mo" },
    { days: 345, long: "year", short: "y" },
    { days: 364, long: "year", short: "y" },
    { days: 365, long: "year", short: "y" },
    { days: 1000, long: "year", short: "y" },
  ];

  it.each(EXPECTED_UNIT)(
    "picks the same count and unit at $days days",
    ({ days, long, short }) => {
      const ago = formatAgo(daysAgo(days), NOW);
      const age = formatAge(daysAgo(days), NOW);

      // Same leading number, whatever it is — that is the part the two used to
      // disagree on once the month cap existed in only one of them.
      const count = (s: string) => s.split(" ")[0];
      expect(count(age)).toBe(count(ago));

      const n = Number(count(ago));
      expect(ago).toBe(`${n} ${long}${n === 1 ? "" : "s"} ago`);
      expect(age).toBe(`${n} ${short} ago`);
    },
  );
});

// The wire carries ffprobe's raw values so these mappings are the only place
// display wording lives; they can change without a migration.
describe("codecLabel", () => {
  it("names the codecs people recognise", () => {
    expect(codecLabel("h264")).toBe("H.264");
    expect(codecLabel("vp9")).toBe("VP9");
    expect(codecLabel("aac")).toBe("AAC");
    expect(codecLabel("opus")).toBe("Opus");
  });

  // Which spelling ffprobe reports depends on the container it read, so the
  // aliases must land on the same label rather than leaking two names for one
  // codec into the UI.
  it("collapses the container-dependent aliases", () => {
    expect(codecLabel("avc1")).toBe(codecLabel("h264"));
    expect(codecLabel("mp4a")).toBe(codecLabel("aac"));
    expect(codecLabel("av01")).toBe(codecLabel("av1"));
    expect(codecLabel("hev1")).toBe("H.265");
  });

  it("is case-insensitive", () => {
    expect(codecLabel("H264")).toBe("H.264");
    expect(codecLabel("AAC")).toBe("AAC");
  });

  // A codec peeq has never seen is more useful shown than hidden.
  it("falls back to the raw name uppercased", () => {
    expect(codecLabel("someneWcodec")).toBe("SOMENEWCODEC");
  });

  it("renders nothing for a missing value", () => {
    expect(codecLabel(undefined)).toBe("");
    expect(codecLabel("")).toBe("");
  });
});

describe("resolutionLabel", () => {
  it("uses the p form for the ordinary heights", () => {
    expect(resolutionLabel(1080)).toBe("1080p");
    expect(resolutionLabel(720)).toBe("720p");
    expect(resolutionLabel(1440)).toBe("1440p");
  });

  it("uses the names people say for the two heights that have one", () => {
    expect(resolutionLabel(2160)).toBe("4K");
    expect(resolutionLabel(4320)).toBe("8K");
  });

  // Matched exactly, not by threshold: a `>= 2160` test labels an 8K file
  // "4K" — the same class of lie the strip replaced format_used to avoid.
  it("does not sweep taller heights into the 4K label", () => {
    expect(resolutionLabel(2880)).toBe("2880p");
    expect(resolutionLabel(4320)).not.toBe("4K");
  });

  // Odd heights are reported as they are rather than rounded into a lie.
  it("does not round a non-standard height", () => {
    expect(resolutionLabel(1082)).toBe("1082p");
  });

  it("renders nothing for a missing or nonsense height", () => {
    expect(resolutionLabel(undefined)).toBe("");
    expect(resolutionLabel(0)).toBe("");
    expect(resolutionLabel(-1)).toBe("");
  });
});

describe("formatSize", () => {
  it("keeps one decimal for GB and whole units below", () => {
    expect(formatSize(1.4 * 1024 ** 3)).toBe("1.4 GB");
    expect(formatSize(412 * 1024 ** 2)).toBe("412 MB");
    expect(formatSize(8 * 1024)).toBe("8 KB");
    expect(formatSize(512)).toBe("512 B");
  });

  // The stat strip drops a column whose value is empty, so an unknown size
  // has to read as empty rather than "0 B".
  it("renders nothing for a missing size", () => {
    expect(formatSize(undefined)).toBe("");
    expect(formatSize(0)).toBe("");
  });
});

// The title slot for a video added by URL, which is queued before anything is
// known about it. One helper for Up next and the Library card, so the two
// cannot describe the same wait differently.
describe("videoLabel", () => {
  it("uses the video's own title whenever there is one", () => {
    const label = videoLabel("But what is a neural network?");
    expect(label.text).toBe("But what is a neural network?");
    expect(label.placeholder).toBe(false);
    expect(label.pending).toBe(false);
  });

  it("ignores the state once a title has arrived", () => {
    expect(videoLabel("Real title", "failed").text).toBe("Real title");
  });

  it("treats a whitespace-only title as no title at all", () => {
    expect(videoLabel("   ").placeholder).toBe(true);
  });

  it("says the details are being read while the queue is moving", () => {
    const label = videoLabel("");
    expect(label.text).toBe("Reading details from YouTube");
    expect(label.pending).toBe(true);
    expect(label.placeholder).toBe(true);
  });

  // Pending drives the pulse, and a pulse means work is happening. With the
  // download worker stopped nothing is being read, so the wording drops the
  // claim and the pulse goes with it.
  it("stops claiming a fetch is happening while the queue is stalled", () => {
    const label = videoLabel("", "stalled");
    expect(label.text).toBe("Waiting to read details");
    expect(label.pending).toBe(false);
    expect(label.placeholder).toBe(true);
  });

  it("stops waiting altogether once the download has failed", () => {
    const label = videoLabel("", "failed");
    expect(label.text).toBe("Details unavailable");
    expect(label.pending).toBe(false);
    expect(label.placeholder).toBe(true);
  });
});

// videos.id IS the YouTube id, so both forms are built from the row alone.
describe("watchURL / shortWatchLink", () => {
  it("builds a watch URL and its short display form from an id", () => {
    expect(watchURL("aircAruvnKk")).toBe(
      "https://www.youtube.com/watch?v=aircAruvnKk",
    );
    expect(shortWatchLink("aircAruvnKk")).toBe("youtu.be/aircAruvnKk");
  });
});
