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

  it("seeks when a cue is clicked", async () => {
    const seek = vi.fn();
    render(
      <TranscriptCard
        vttUrl="/api/videos/v1/subtitles"
        filenameBase="v"
        seek={seek}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));
    await userEvent.click(await screen.findByText(/Fuse plugs/));

    expect(seek).toHaveBeenCalledWith(5);
  });

  // Without a seek there is no media to jump to, so a cue must not be a
  // control: a button that looked identical and did nothing would be worse
  // than plain text. This is the inbox video page's case.
  it("renders inert cues when there is nothing to seek", async () => {
    render(
      <TranscriptCard vttUrl="/api/videos/v1/subtitles" filenameBase="v" />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));

    const cue = await screen.findByText(/Fuse plugs/);
    const row = cue.closest(".cue") as HTMLElement;
    expect(row.tagName).toBe("DIV");
    expect(row.className).toContain("inert");
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
