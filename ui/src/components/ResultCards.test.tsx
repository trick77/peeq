import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";
import { ResultCards, type ResultCardGroup } from "./ResultCards";
import type { SearchMatch } from "../api/search";

// A search hit can be a video peeq only ever read: captions fetched,
// summarized and indexed, the file never downloaded. It is reachable from
// Search and nowhere else, so the card is the only place the difference can be
// drawn before the user clicks.
function group(
  status: string,
  matches: SearchMatch[] = [
    {
      start_seconds: 42,
      snippet: "sodium and cramping",
      distance: 0.1,
      kind: "transcript",
    },
  ],
): ResultCardGroup[] {
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
      matches,
    },
  ];
}

function renderCards(
  results: ResultCardGroup[],
  handlers: {
    onOpen?: (id: string, s: number) => void;
    onOpenVideo?: (id: string) => void;
    onOpenChannel?: (id: string) => void;
  } = {},
) {
  return render(
    <ResultCards
      results={results}
      onOpen={handlers.onOpen ?? vi.fn()}
      onOpenVideo={handlers.onOpenVideo ?? vi.fn()}
      onOpenChannel={handlers.onOpenChannel ?? vi.fn()}
    />,
  );
}

describe("ResultCards", () => {
  it("badges a video peeq read but never downloaded", () => {
    const { container } = renderCards(group("new"));

    // Not "Summary only": such a video keeps its transcript, and this very card
    // lists transcript moments underneath. What it is missing is the file.
    expect(screen.getByText("Not downloaded")).toBeInTheDocument();
    expect(screen.queryByText("Summary only")).not.toBeInTheDocument();
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
    const { container } = renderCards(group("downloaded"));

    expect(screen.queryByText("Not downloaded")).not.toBeInTheDocument();
    expect(container.querySelector(".play")).toBeInTheDocument();
    expect(container.querySelector("img.fill")).toHaveAttribute(
      "src",
      "/api/videos/v1/thumbnail",
    );
  });

  it("opens the video with no seek from the title and the thumbnail", async () => {
    const onOpen = vi.fn();
    const onOpenVideo = vi.fn();
    renderCards(group("downloaded"), { onOpen, onOpenVideo });

    await userEvent.click(
      screen.getByRole("button", { name: "Why Athletes Cramp" }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Open Why Athletes Cramp" }),
    );

    // Both go through onOpenVideo. Never onOpen(id, 0) — Player applies any
    // seekTo that is not undefined, so a zero would rewind a half-watched video
    // and the next resume flush would store that zero.
    expect(onOpenVideo).toHaveBeenCalledTimes(2);
    expect(onOpenVideo).toHaveBeenCalledWith("v1");
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("navigates to the channel from the channel name", async () => {
    const onOpenChannel = vi.fn();
    renderCards(group("downloaded"), { onOpenChannel });

    await userEvent.click(screen.getByRole("button", { name: "Attia" }));

    expect(onOpenChannel).toHaveBeenCalledWith("c1");
  });

  it("jumps to the moment for a timestamped match", async () => {
    const onOpen = vi.fn();
    renderCards(group("downloaded"), { onOpen });

    await userEvent.click(screen.getByText("sodium and cramping"));

    expect(onOpen).toHaveBeenCalledWith("v1", 42);
  });

  it("puts the summary match first and opens it without a seek", async () => {
    const onOpen = vi.fn();
    const onOpenVideo = vi.fn();
    const { container } = renderCards(
      group("downloaded", [
        {
          start_seconds: 42,
          snippet: "sodium and cramping",
          distance: 0.1,
          kind: "transcript",
        },
        {
          start_seconds: 0,
          snippet: "a summary of the whole video",
          distance: 0.2,
          kind: "summary",
        },
        {
          start_seconds: 300,
          snippet: "the chapter",
          distance: 0.3,
          kind: "chapter",
        },
      ]),
      { onOpen, onOpenVideo },
    );

    // Retrieval ranked the summary second; the card leads with it, and the rest
    // keep the order they arrived in.
    const rows = Array.from(container.querySelectorAll(".match .snip")).map(
      (n) => n.textContent,
    );
    expect(rows).toEqual([
      "a summary of the whole video",
      "sodium and cramping",
      "the chapter",
    ]);

    // A summary chunk is stored at start_seconds 0, so seeking to it would
    // rewind the video and then overwrite the stored resume position with 0.
    await userEvent.click(screen.getByText("a summary of the whole video"));
    expect(onOpenVideo).toHaveBeenCalledWith("v1");
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("labels a chapter and a summary but never a transcript moment", () => {
    renderCards(
      group("downloaded", [
        {
          start_seconds: 0,
          snippet: "the summary",
          distance: 0.1,
          kind: "summary",
        },
        {
          start_seconds: 42,
          snippet: "a moment",
          distance: 0.2,
          kind: "transcript",
        },
        {
          start_seconds: 300,
          snippet: "a chapter",
          distance: 0.3,
          kind: "chapter",
        },
      ]),
    );

    expect(screen.getByText("Summary")).toBeInTheDocument();
    expect(screen.getByText("Chapter")).toBeInTheDocument();
    // The timestamp beside it already says what the row is.
    expect(screen.queryByText("Transcript")).not.toBeInTheDocument();
  });
});
