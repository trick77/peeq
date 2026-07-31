import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { ResultCards, type ResultCardGroup } from "./ResultCards";

// A search hit can be a video peeq only ever read: captions fetched,
// summarized and indexed, the file never downloaded. It is reachable from
// Search and nowhere else, so the card is the only place the difference can be
// drawn before the user clicks.
function group(status: string): ResultCardGroup[] {
  return [
    {
      video: {
        id: "v1",
        title: "Why Athletes Cramp",
        channel_id: "c1",
        channel_name: "Attia",
        duration_seconds: 600,
        has_thumbnail: true,
        status,
      },
      matches: [
        {
          start_seconds: 42,
          snippet: "sodium and cramping",
          distance: 0.1,
          kind: "transcript",
        },
      ],
    },
  ];
}

describe("ResultCards", () => {
  it("badges a video peeq read but never downloaded", () => {
    const { container } = render(
      <ResultCards results={group("new")} onOpen={vi.fn()} />,
    );

    expect(screen.getByText("Summary only")).toBeInTheDocument();
    // No play glyph: the card leads to a summary page, not a player.
    expect(container.querySelector(".play")).toBeNull();
    // Its only poster is the one cached for the Inbox — the videos-row
    // endpoint has nothing to serve for it.
    expect(container.querySelector("img.fill")).toHaveAttribute(
      "src",
      "/api/pending/v1/thumbnail",
    );
  });

  it("leaves a downloaded video's card alone", () => {
    const { container } = render(
      <ResultCards results={group("downloaded")} onOpen={vi.fn()} />,
    );

    expect(screen.queryByText("Summary only")).not.toBeInTheDocument();
    expect(container.querySelector(".play")).toBeInTheDocument();
    expect(container.querySelector("img.fill")).toHaveAttribute(
      "src",
      "/api/videos/v1/thumbnail",
    );
  });
});
