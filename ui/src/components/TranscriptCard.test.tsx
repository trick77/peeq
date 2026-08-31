import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TranscriptCard } from "./TranscriptCard";

const VTT =
  "WEBVTT\n\n" +
  "00:00:05.000 --> 00:00:07.000\nFuse plugs are meant to wash away.\n\n" +
  "00:01:12.000 --> 00:01:15.000\nTeton emptied in six hours.\n";

describe("TranscriptCard", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, text: async () => VTT }),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // Nothing is fetched until the panel is opened: a transcript is a file the
  // page does not otherwise need, on every video page in the app.
  it("loads nothing until it is opened", async () => {
    render(
      <TranscriptCard vttUrl="/api/videos/v1/subtitles" filenameBase="v" />,
    );
    expect(fetch).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));

    await screen.findByText(/Fuse plugs are meant to wash away/);
    expect(fetch).toHaveBeenCalledWith("/api/videos/v1/subtitles");
  });

  it("offers both downloads and a copy button", async () => {
    render(
      <TranscriptCard
        vttUrl="/api/videos/v1/subtitles"
        filenameBase="my_video"
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));
    await screen.findByText(/Fuse plugs/);

    expect(screen.getByRole("button", { name: /\.txt/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Copy text/ })).toBeTruthy();

    // The .vtt is a plain link to the same URL the panel parsed, named from
    // the title alone — never the video id or a share token.
    const vtt = screen.getByRole("link", { name: /\.vtt/ });
    expect(vtt.getAttribute("href")).toBe("/api/videos/v1/subtitles");
    expect(vtt.getAttribute("download")).toBe("my_video.vtt");
  });

  it("puts the transcript on the clipboard and confirms on the button", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });

    render(
      <TranscriptCard vttUrl="/api/videos/v1/subtitles" filenameBase="v" />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));
    await screen.findByText(/Fuse plugs/);
    await userEvent.click(screen.getByRole("button", { name: /Copy text/ }));

    // Exactly what the .txt download holds: the de-duplicated cue text, with
    // no timestamps — not the raw rolling-window VTT.
    expect(writeText).toHaveBeenCalledWith(
      "Fuse plugs are meant to wash away.\nTeton emptied in six hours.",
    );
    expect(await screen.findByText("Copied")).toBeTruthy();
  });

  it("seeks from the timestamp, not from the words", async () => {
    const seek = vi.fn();
    render(
      <TranscriptCard
        vttUrl="/api/videos/v1/subtitles"
        filenameBase="v"
        seek={seek}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));

    // Reading is not jumping. Clicking the line used to move the video, which
    // made the transcript hostile to read and — because a selection cannot
    // cross two form controls — impossible to drag-select across.
    await userEvent.click(await screen.findByText(/Fuse plugs/));
    expect(seek).not.toHaveBeenCalled();

    await userEvent.click(
      screen.getByRole("button", { name: /Play from 0:05/ }),
    );
    expect(seek).toHaveBeenCalledWith(5);
  });

  // The line itself is never a control, so the whole cue list is one continuous
  // run of selectable text.
  it("leaves the spoken words out of any control", async () => {
    render(
      <TranscriptCard
        vttUrl="/api/videos/v1/subtitles"
        filenameBase="v"
        seek={vi.fn()}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));

    const line = await screen.findByText(/Fuse plugs/);
    expect(line.closest("button")).toBeNull();
    expect((line.closest(".cue") as HTMLElement).tagName).toBe("DIV");
  });

  // Without a seek there is no media to jump to, so the stamp must not be a
  // control either: a button that looked identical and did nothing would be
  // worse than plain text. This is the inbox video page's case.
  it("renders a plain timestamp when there is nothing to seek", async () => {
    render(
      <TranscriptCard vttUrl="/api/videos/v1/subtitles" filenameBase="v" />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));

    await screen.findByText(/Fuse plugs/);
    expect(screen.queryByRole("button", { name: /Play from/ })).toBeNull();
    expect(screen.getByText("0:05").tagName).toBe("SPAN");
  });

  describe("find", () => {
    async function openAndSearch(term: string) {
      render(
        <TranscriptCard
          vttUrl="/api/videos/v1/subtitles"
          filenameBase="v"
          seek={vi.fn()}
        />,
      );
      await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));
      await screen.findByText(/Fuse plugs/);
      await userEvent.type(
        screen.getByPlaceholderText(/Find in transcript/),
        term,
      );
    }

    // The counter used to read matching lines over TOTAL lines, so a long
    // transcript said "1 / 5413" — a number that looks like a position among
    // 5413 matches while being neither.
    it("counts the position among matches, not the lines in the file", async () => {
      await openAndSearch("e");

      expect(screen.getByText("1 / 2")).toBeInTheDocument();
    });

    it("steps through matches and wraps", async () => {
      await openAndSearch("e");

      await userEvent.click(screen.getByRole("button", { name: "Next match" }));
      expect(screen.getByText("2 / 2")).toBeInTheDocument();
      // Wrapping, not stopping: the counter says where you are, so a lap
      // cannot be mistaken for a dead button.
      await userEvent.click(screen.getByRole("button", { name: "Next match" }));
      expect(screen.getByText("1 / 2")).toBeInTheDocument();
      await userEvent.click(
        screen.getByRole("button", { name: "Previous match" }),
      );
      expect(screen.getByText("2 / 2")).toBeInTheDocument();
    });

    it("marks the match it is parked on", async () => {
      await openAndSearch("e");

      // By position, not by text: a match splits the line into <mark> and
      // plain fragments, so getByText no longer sees it as one node.
      const first = document.querySelector('[data-cue="0"]')!;
      const second = document.querySelector('[data-cue="1"]')!;
      expect(first.className).toContain("current");
      expect(second.className).not.toContain("current");

      await userEvent.click(screen.getByRole("button", { name: "Next match" }));
      expect(second.className).toContain("current");
      expect(first.className).not.toContain("current");
    });

    it("says so when nothing matches, and greys the steppers", async () => {
      await openAndSearch("zzz");

      expect(screen.getByText("None")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Next match" })).toBeDisabled();
    });

    // A second search starts at its own first match rather than wherever the
    // previous one left the cursor — and a cursor pointing past a shrunken
    // list must never render as "2 / 1".
    it("restarts at the first match when the term changes", async () => {
      await openAndSearch("e");
      await userEvent.click(screen.getByRole("button", { name: "Next match" }));
      expect(screen.getByText("2 / 2")).toBeInTheDocument();

      await userEvent.type(
        screen.getByPlaceholderText(/Find in transcript/),
        "mptied",
      );
      expect(screen.getByText("1 / 1")).toBeInTheDocument();
    });

    it("steps with Enter and back with Shift+Enter", async () => {
      await openAndSearch("e");

      const box = screen.getByPlaceholderText(/Find in transcript/);
      await userEvent.type(box, "{Enter}");
      expect(screen.getByText("2 / 2")).toBeInTheDocument();
      await userEvent.type(box, "{Shift>}{Enter}{/Shift}");
      expect(screen.getByText("1 / 2")).toBeInTheDocument();
    });
  });

  // The share page keeps this component mounted across a token change, so a
  // cached transcript must never outlive the URL it came from — one video's
  // words under another video's title.
  it("drops a loaded transcript when the URL changes", async () => {
    const { rerender } = render(
      <TranscriptCard vttUrl="/api/s/tokenA/subtitles" filenameBase="v" />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));
    await screen.findByText(/Fuse plugs/);

    vi.mocked(fetch).mockResolvedValue({
      ok: true,
      text: async () =>
        "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nA different video entirely.\n",
    } as Response);
    rerender(
      <TranscriptCard vttUrl="/api/s/tokenB/subtitles" filenameBase="v" />,
    );

    await waitFor(() =>
      expect(screen.queryByText(/Fuse plugs/)).not.toBeInTheDocument(),
    );
    expect(await screen.findByText(/A different video entirely/)).toBeTruthy();
  });

  it("says so when the transcript cannot be loaded", async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: false } as Response);
    render(
      <TranscriptCard vttUrl="/api/videos/v1/subtitles" filenameBase="v" />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));

    expect(await screen.findByText("Failed to load transcript.")).toBeTruthy();
  });
});
