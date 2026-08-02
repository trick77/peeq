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

  // The other fileless hit: downloaded once, then swept to reclaim the disk.
  // Everything but the media survives, which is why it is still findable here —
  // and why its card must not draw a play triangle over a file that is gone.
  it("badges a video whose file was reclaimed, and offers no play", () => {
    const { container } = renderCards(group("tombstoned"));

    expect(screen.getByText("Removed")).toBeInTheDocument();
    // Not "Not downloaded" — this one WAS downloaded, and its page offers
    // Re-download rather than Download.
    expect(screen.queryByText("Not downloaded")).not.toBeInTheDocument();
    expect(container.querySelector(".play")).toBeNull();
    // It keeps its own poster, unlike a video peeq only ever read.
    expect(container.querySelector("img.fill")).toHaveAttribute(
      "src",
      "/api/videos/v1/thumbnail",
    );
  });

  it("leaves a downloaded video's card alone", () => {
    const { container } = renderCards(group("downloaded"));

    expect(screen.queryByText("Not downloaded")).not.toBeInTheDocument();
    expect(screen.queryByText("Removed")).not.toBeInTheDocument();
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
    // follow by timestamp.
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

  // The timestamp says what a chapter row and a transcript row are; the label
  // said it again, in a word that never changed what the reader would do. Only
  // the summary keeps one, and it is the row with no timestamp to show.
  it("labels a summary but never a chapter or a transcript moment", () => {
    const { container } = renderCards(
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
    expect(screen.queryByText("Chapter")).not.toBeInTheDocument();
    expect(screen.queryByText("Transcript")).not.toBeInTheDocument();
    // The chapter row lost the word, not its timestamp.
    expect(container).toHaveTextContent("5:00");
  });

  // Retrieval order is bm25 rank or citation order — neither of which the
  // reader can see. The timestamps they CAN see were running backwards: a
  // chapter hit late in the video sat above a transcript hit near the start.
  it("orders moments by timestamp regardless of which lane found them", () => {
    const { container } = renderCards(
      group("downloaded", [
        {
          start_seconds: 842,
          snippet: "the chapter",
          distance: 0.1,
          kind: "chapter",
        },
        {
          start_seconds: 191,
          snippet: "an early moment",
          distance: 0.2,
          kind: "transcript",
        },
        {
          start_seconds: 0,
          snippet: "the summary",
          distance: 0.3,
          kind: "summary",
        },
        {
          start_seconds: 402,
          snippet: "a middle moment",
          distance: 0.4,
          kind: "transcript",
        },
      ]),
    );

    const rows = Array.from(container.querySelectorAll(".match .snip")).map(
      (n) => n.textContent,
    );
    // The summary has no timestamp of its own — it is stored at 0 — so it is
    // hoisted rather than sorted into place.
    expect(rows).toEqual([
      "the summary",
      "an early moment",
      "a middle moment",
      "the chapter",
    ]);
  });

  // Two moments at the same second keep the order they arrived in rather than
  // swapping between renders.
  it("keeps retrieval order for moments at the same second", () => {
    const { container } = renderCards(
      group("downloaded", [
        {
          start_seconds: 120,
          snippet: "found first",
          distance: 0.1,
          kind: "chapter",
        },
        {
          start_seconds: 120,
          snippet: "found second",
          distance: 0.2,
          kind: "transcript",
        },
      ]),
    );

    const rows = Array.from(container.querySelectorAll(".match .snip")).map(
      (n) => n.textContent,
    );
    expect(rows).toEqual(["found first", "found second"]);
  });

  // The same byline every other card in the app carries. The date the video
  // entered the archive is deliberately absent — see VideoCard.
  it("shows when the video aired, and nothing when it never learned", () => {
    const aired = group("downloaded");
    aired[0].video.published_at = new Date(
      Date.now() - 3 * 86400 * 1000,
    ).toISOString();
    const { container } = renderCards(aired);

    expect(container.querySelector(".ch")).toHaveTextContent(
      "aired 3 days ago",
    );
    expect(container.querySelector(".ch")).not.toHaveTextContent("added");
  });

  it("stops the byline at the channel when there is no air date", () => {
    const { container } = renderCards(group("downloaded"));

    expect(container.querySelector(".ch")).not.toHaveTextContent("aired");
    expect(container.querySelector(".ch .dot")).toBeNull();
  });
});
