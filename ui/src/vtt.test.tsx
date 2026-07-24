import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  highlightCue,
  matchesFind,
  parseVtt,
  transcriptFilenameBase,
  transcriptToText,
} from "./vtt";

// The sound-event stripping rules themselves are exercised from
// Player.test.tsx, which has covered them since they lived in that file. These
// pin the parts the share page leans on.

describe("parseVtt timestamps", () => {
  it("reads HH:MM:SS.mmm", () => {
    const vtt = "WEBVTT\n\n01:02:03.000 --> 01:02:05.000\nhello\n";
    expect(parseVtt(vtt)).toEqual([{ ts: 3723, text: "hello" }]);
  });

  it("reads the MM:SS.mmm short form yt-dlp also emits", () => {
    const vtt = "WEBVTT\n\n02:03.500 --> 02:05.000\nhello\n";
    // Fractional seconds ride along; seek() and formatDuration both take them.
    expect(parseVtt(vtt)).toEqual([{ ts: 123.5, text: "hello" }]);
  });

  it("returns nothing for text with no timing lines at all", () => {
    expect(parseVtt("WEBVTT\n\nnot a transcript\n")).toEqual([]);
  });
});

describe("transcriptFilenameBase", () => {
  it("makes a filesystem-safe name from the title", () => {
    expect(transcriptFilenameBase("How the CIA writes: a threat!")).toBe(
      "How_the_CIA_writes_a_threat_",
    );
  });

  it("falls back to a generic name rather than any identifier", () => {
    // The old signature fell back to the video id, which on peeq IS the
    // YouTube id. An untitled video must not put that in a Downloads folder.
    expect(transcriptFilenameBase("")).toBe("transcript");
    // Punctuation-only survives as its own separator, which is still a safe
    // name and still names nothing.
    expect(transcriptFilenameBase("///")).toBe("_");
  });

  it("caps the length so a long title cannot make an unusable filename", () => {
    expect(transcriptFilenameBase("a".repeat(200))).toHaveLength(80);
  });
});

describe("matchesFind / highlightCue", () => {
  it("ignores an empty or whitespace-only query", () => {
    expect(matchesFind("anything", "")).toBe(false);
    expect(matchesFind("anything", "   ")).toBe(false);
  });

  it("matches case-insensitively", () => {
    expect(matchesFind("Confidence is a discipline", "DISCIPLINE")).toBe(true);
  });

  it("returns the text unchanged when nothing matches", () => {
    expect(highlightCue("plain text", "absent")).toBe("plain text");
  });

  it("wraps every occurrence in <mark>, regex metacharacters included", () => {
    const { container } = render(
      <div>{highlightCue("a.b and a.b", "a.b")}</div>,
    );
    expect(container.querySelectorAll("mark")).toHaveLength(2);
    // The dot is escaped, so it does not match "axb".
    render(<div>{highlightCue("axb", "a.b")}</div>);
    expect(screen.queryByText("axb")).toBeInTheDocument();
  });
});

describe("transcriptToText", () => {
  it("joins cue text one line per cue, dropping timestamps", () => {
    expect(
      transcriptToText([
        { ts: 1, text: "first" },
        { ts: 9, text: "second" },
      ]),
    ).toBe("first\nsecond");
  });
});
