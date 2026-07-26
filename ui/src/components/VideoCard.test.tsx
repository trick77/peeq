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

  // A video added by URL is enqueued before its metadata is known, so its row
  // carries no title until the download worker's preflight resolves one. Left
  // unhandled the card renders an empty <h3> and an "Open " aria-label with
  // nothing after it — an unlabelled control. The id is the same fallback the
  // backend uses for a preflight-failed video's Activity subject.
  it("falls back to the id while a video still has no title", () => {
    renderCard({ title: "" });
    expect(
      screen.getByRole("button", { name: "Open dQw4w9WgXcQ" }),
    ).toBeInTheDocument();
    const title = screen.getByRole("button", { name: "dQw4w9WgXcQ" });
    expect(title).toHaveTextContent("dQw4w9WgXcQ");
    // The fallback must reach the visible heading, not just the label.
    expect(title.closest("h3")).not.toBeNull();
  });
});
