import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DetailsCard } from "./DetailsCard";
import type { Video, VideoEmbeddings } from "../../api/types";

function stats(overrides: Partial<VideoEmbeddings> = {}): VideoEmbeddings {
  return {
    model: "text-embedding-3-small",
    dimensions: 1536,
    chunks: 68,
    tokens: 38412,
    kinds: [
      { kind: "transcript", count: 54, tokens: 34000 },
      { kind: "chapter", count: 13, tokens: 4112 },
      { kind: "summary", count: 1, tokens: 300 },
    ],
    ...overrides,
  };
}

// A fully populated video: probed, downloaded after migration 0009, watched.
// The near-empty case gets its own test — it is the one most of a real library
// hits, since nothing backfills the YouTube columns onto older rows.
function baseVideo(overrides: Partial<Video> = {}): Video {
  return {
    id: "dQw4w9WgXcQ",
    url: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
    title: "The Man Who Broke Maths",
    channel_id: "UC1",
    channel_name: "Veritasium",
    duration_seconds: 2052,
    has_thumbnail: true,
    has_media: true,
    filesize_bytes: 432013312,
    media_container: "mp4",
    video_codec: "h264",
    video_height: 1080,
    audio_codec: "aac",
    availability: "available",
    status: "downloaded",
    watched: true,
    watched_at: "2026-08-06T10:00:00Z",
    resume_position_seconds: 0,
    state_version: 1,
    favorite: false,
    downloaded_at: "2026-08-01T10:00:00Z",
    summary: "",
    chapters: [],
    key_points: [],
    summary_status: "done",
    indexed: true,
    audio_language: "en",
    has_subtitles: true,
    category: "science",
    ...overrides,
  };
}

function open(video: Video, s: VideoEmbeddings | null = null) {
  return render(
    <DetailsCard video={video} stats={s} open onToggle={() => {}} />,
  );
}

describe("DetailsCard", () => {
  describe("collapsed", () => {
    it("shows the label and a glance line, and none of the rows", () => {
      render(
        <DetailsCard
          video={baseVideo()}
          stats={stats()}
          open={false}
          onToggle={() => {}}
        />,
      );

      expect(screen.getByText("Details")).toBeInTheDocument();
      expect(screen.getByText("34:12 · 412 MB · 1080p")).toBeInTheDocument();
      // The whole point of the redesign: nothing but the line at rest.
      expect(screen.queryByText("Container")).not.toBeInTheDocument();
      expect(screen.queryByText("Video ID")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: /Details/ })).toHaveAttribute(
        "aria-expanded",
        "false",
      );
    });

    // The control is visible and operable at rest — it never hides behind a
    // hover, and it is a real button, so it is reachable by keyboard.
    it("toggles when clicked", async () => {
      const onToggle = vi.fn();
      render(
        <DetailsCard
          video={baseVideo()}
          stats={null}
          open={false}
          onToggle={onToggle}
        />,
      );

      await userEvent.click(screen.getByRole("button", { name: /Details/ }));

      expect(onToggle).toHaveBeenCalledTimes(1);
    });

    // formatSize and resolutionLabel both return "" for a missing value, so the
    // glance must not render " ·  · " around the holes.
    it("drops what an unprobed video does not have", () => {
      render(
        <DetailsCard
          video={baseVideo({ media_container: "", video_height: 0 })}
          stats={null}
          open={false}
          onToggle={() => {}}
        />,
      );

      expect(screen.getByText("34:12 · 412 MB")).toBeInTheDocument();
    });
  });

  describe("expanded", () => {
    it("reports the file as ffprobe measured it", () => {
      open(baseVideo());

      expect(screen.getByText("File")).toBeInTheDocument();
      expect(screen.getByText("34:12")).toBeInTheDocument();
      expect(screen.getByText("412 MB")).toBeInTheDocument();
      expect(screen.getByText("MP4")).toBeInTheDocument();
      expect(screen.getByText("1080p H.264")).toBeInTheDocument();
      expect(screen.getByText("AAC")).toBeInTheDocument();
      expect(screen.getByText("English")).toBeInTheDocument();
    });

    // The one computed figure: 432013312 bytes over 2052 seconds is 1.7 Mbps.
    it("computes an average bitrate from size and length", () => {
      open(baseVideo());

      expect(screen.getByText("Average bitrate")).toBeInTheDocument();
      expect(screen.getByText("1.7 Mbps")).toBeInTheDocument();
    });

    // has_subtitles is a bool and nothing on the wire says what language the
    // subtitles are in — so this row must never borrow the audio language.
    it("answers Subtitles yes or no without claiming a language", () => {
      open(baseVideo({ audio_language: "de", has_subtitles: false }));

      expect(screen.getByText("Subtitles")).toBeInTheDocument();
      expect(screen.getByText("No")).toBeInTheDocument();
      expect(screen.getByText("German")).toBeInTheDocument();
      // "German" belongs to Audio language, and is the only place it appears.
      expect(screen.getAllByText("German")).toHaveLength(1);
    });

    it("reports where the copy came from", () => {
      open(
        baseVideo({
          yt_categories: ["Science & Technology"],
          sponsorblock_segments: [
            { category: "sponsor", start_time: 10, end_time: 20 },
            { category: "intro", start_time: 0, end_time: 5 },
          ],
        }),
      );

      expect(screen.getByText("Source")).toBeInTheDocument();
      expect(screen.getByText("Science & Technology")).toBeInTheDocument();
      expect(screen.getByText("2 segments")).toBeInTheDocument();
      expect(screen.getByText("dQw4w9WgXcQ")).toBeInTheDocument();
      expect(screen.getByText("Watched")).toBeInTheDocument();
    });

    it("uses the singular for a lone SponsorBlock segment", () => {
      open(
        baseVideo({
          sponsorblock_segments: [
            { category: "sponsor", start_time: 10, end_time: 20 },
          ],
        }),
      );

      expect(screen.getByText("1 segment")).toBeInTheDocument();
    });

    // Availability and live_status were both mocked up and both cut: the
    // column only ever holds 'available' or 'unknown', is written once at
    // metadata time and never refreshed, so the row could only mislead.
    it("does not report availability or live status", () => {
      open(baseVideo({ availability: "available", live_status: "was_live" }));

      expect(screen.queryByText("On YouTube")).not.toBeInTheDocument();
      expect(screen.queryByText("Kind")).not.toBeInTheDocument();
      expect(screen.queryByText("Was live")).not.toBeInTheDocument();
    });

    // format_used is the resolved yt-dlp -f selector: what was ASKED FOR, the
    // same string for every video downloaded under one preset. It had a pill
    // here once and is deliberately gone; this guards that decision.
    it("never shows the yt-dlp format selector", () => {
      open(
        baseVideo({
          format_used:
            "bestvideo[height<=1080][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4",
        }),
      );

      expect(screen.queryByText(/bestvideo/)).not.toBeInTheDocument();
    });

    it("shows the first tags and counts the rest", () => {
      open(
        baseVideo({
          yt_tags: ["a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"],
        }),
      );

      expect(screen.getByText("a")).toBeInTheDocument();
      expect(screen.getByText("h")).toBeInTheDocument();
      expect(screen.queryByText("i")).not.toBeInTheDocument();
      expect(screen.getByText("+3")).toBeInTheDocument();
    });

    it("names every chunk kind and the model that wrote them", () => {
      open(baseVideo(), stats());

      expect(screen.getByText("Search index")).toBeInTheDocument();
      expect(screen.getByText("Transcript")).toBeInTheDocument();
      expect(screen.getByText("Chapters")).toBeInTheDocument();
      expect(screen.getByText("54")).toBeInTheDocument();
      // Grouped digits, and grouped the same way in every locale: the number
      // formatter is pinned, so a de-CH runner must not see 38’412.
      expect(screen.getByText("38,412")).toBeInTheDocument();
      expect(screen.getByText("text-embedding-3-small")).toBeInTheDocument();
      expect(screen.getByText("1536 dimensions")).toBeInTheDocument();
    });

    // A kind the Go pipeline adds must still show up, under its own word, or
    // the rows would silently stop adding up.
    it("shows an unknown chunk kind rather than dropping it", () => {
      open(
        baseVideo(),
        stats({
          chunks: 2,
          kinds: [{ kind: "keyframe", count: 2, tokens: 10 }],
        }),
      );

      expect(screen.getByText("keyframe")).toBeInTheDocument();
    });

    // The panel opens before the request lands, and a video that was never
    // indexed never makes one — either way the group is absent rather than
    // empty or apologetic.
    it("leaves the index group out entirely when there is nothing indexed", () => {
      open(baseVideo(), { chunks: 0, tokens: 0, kinds: [] });

      expect(screen.queryByText("Search index")).not.toBeInTheDocument();
      expect(screen.queryByText("Tokens embedded")).not.toBeInTheDocument();
    });

    // The case most of a real library hits: never probed, and downloaded
    // before the migration that added YouTube's own columns. Every missing row
    // goes, and a group with nothing left goes with it — no bare heading.
    it("drops empty rows, and a group left with none of them", () => {
      open(
        baseVideo({
          filesize_bytes: undefined,
          media_container: "",
          video_codec: "",
          video_height: 0,
          audio_codec: "",
          audio_language: "",
          watched: false,
          watched_at: undefined,
          downloaded_at: undefined,
          yt_categories: [],
          yt_tags: [],
        }),
        null,
      );

      // What survives: length, the Subtitles bool, and the id.
      expect(screen.getByText("34:12")).toBeInTheDocument();
      expect(screen.getByText("Subtitles")).toBeInTheDocument();
      expect(screen.getByText("dQw4w9WgXcQ")).toBeInTheDocument();
      // What does not.
      expect(screen.queryByText("Size")).not.toBeInTheDocument();
      expect(screen.queryByText("Container")).not.toBeInTheDocument();
      expect(screen.queryByText("Average bitrate")).not.toBeInTheDocument();
      expect(screen.queryByText("Added")).not.toBeInTheDocument();
      expect(screen.queryByText("Watched")).not.toBeInTheDocument();
      expect(screen.queryByText("YouTube category")).not.toBeInTheDocument();
      // Source still has the id, so it stays; the index group is the one that
      // has nothing at all.
      expect(screen.getByText("Source")).toBeInTheDocument();
      expect(screen.queryByText("Search index")).not.toBeInTheDocument();
    });
  });
});
