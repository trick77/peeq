import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { VideoCard } from "./VideoCard";
import type { Video } from "../api/types";

const baseVideo: Video = {
  id: "dQw4w9WgXcQ",
  url: "https://youtu.be/dQw4w9WgXcQ",
  title: "The Trillion Dollar Equation",
  channel_id: "chan1",
  channel_name: "Veritasium",
  duration_seconds: 1684,
  has_thumbnail: false,
  has_media: true,
  availability: "available",
  status: "downloaded",
  watched: false,
  resume_position_seconds: 0,
  state_version: 1,
  favorite: false,
  summary: "",
  chapters: [],
  key_points: [],
  summary_status: "pending",
  audio_language: "",
  has_subtitles: false,
  category: "uncategorized",
};

function renderCard(overrides: Partial<Video> = {}) {
  return render(
    <VideoCard
      video={{ ...baseVideo, ...overrides }}
      retentionDays={30}
      onOpen={vi.fn()}
      onToggleFavorite={vi.fn()}
      onToggleWatched={vi.fn()}
    />,
  );
}

describe("VideoCard", () => {
  it("shows the title on both the poster and the title button", () => {
    renderCard();
    expect(
      screen.getByRole("button", { name: "Open The Trillion Dollar Equation" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "The Trillion Dollar Equation" }),
    ).toBeInTheDocument();
  });

  // A video added by URL is enqueued before its metadata is known, so its card
  // carries no title (and no channel) until the download worker's preflight
  // resolves them. What the heading must NOT do is print the raw id and pass it
  // off as a title, which is what it used to do.
  it("says the details are still coming while a video has no title", () => {
    renderCard({ title: "", channel_id: "", channel_name: "" });
    const title = screen.getByRole("button", { name: "dQw4w9WgXcQ" });
    expect(title).toHaveTextContent("Reading details from YouTube");
    // The placeholder must reach the visible heading, not just the label.
    expect(title.closest("h3")).not.toBeNull();
    // The id keeps a place on the card — as a link out to YouTube, the only way
    // to check that the link that was pasted is the video that was queued.
    expect(
      screen.getByRole("link", { name: "youtu.be/dQw4w9WgXcQ" }),
    ).toHaveAttribute("href", "https://www.youtube.com/watch?v=dQw4w9WgXcQ");
    // Both open-the-video controls stay individually named: a grid of untitled
    // cards would otherwise announce every tile with the same sentence.
    expect(
      screen.getByRole("button", { name: "Open dQw4w9WgXcQ" }),
    ).toBeInTheDocument();
  });

  // A download that ended in an error is not waiting for anything — no title is
  // ever coming — so the card stops implying one is on its way.
  it("stops promising details once the download has failed", () => {
    renderCard({ title: "", status: "error" });
    expect(
      screen.getByRole("button", { name: "dQw4w9WgXcQ" }),
    ).toHaveTextContent("Details unavailable");
  });

  // The byline swap is only for a card with nothing else to put there: a video
  // whose channel is already known keeps the channel, title or no title.
  it("keeps the channel in the byline when one is known", () => {
    renderCard({ title: "" });
    expect(screen.getByText("Veritasium")).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: /youtu\.be/ }),
    ).not.toBeInTheDocument();
  });
});
