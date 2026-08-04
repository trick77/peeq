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

describe("parseVtt entity decoding", () => {
  // entitySample / entityWant are shared verbatim with
  // TestParseVTTDecodesEntities in backend/internal/subtitles/vtt_test.go. The
  // panel reads from this parser and the summary from that one, so a difference
  // here is a difference the user sees.
  const entitySample =
    "WEBVTT\n\n" +
    "00:00:01.000 --> 00:00:03.000\n" +
    "Tom &amp; Jerry &gt; everything else\n\n" +
    "00:00:03.000 --> 00:00:05.000\n" +
    "He said &quot;don&#39;t&quot; &amp;lt;not a tag&amp;gt;\n\n" +
    "00:00:05.000 --> 00:00:07.000\n" +
    "spaced&nbsp;out words\n";
  const entityWant =
    'Tom & Jerry > everything else He said "don\'t" ' +
    "&lt;not a tag&gt; spaced out words";

  it("decodes the HTML entities YouTube escapes caption text with", () => {
    // Joined with a space to match how the Go side builds Parsed.Transcript;
    // transcriptToText's own newline format is covered further down.
    expect(
      parseVtt(entitySample)
        .map((c) => c.text)
        .join(" "),
    ).toBe(entityWant);
  });
});

// These mirror the dedup cases in backend/internal/subtitles/vtt_test.go — the
// two parsers have to agree on what the transcript says, so a case added to one
// belongs on the other.
describe("parseVtt rolling-window dedup", () => {
  const texts = (vtt: string) => parseVtt(vtt).map((c) => c.text);

  it("drops leading lines that repeat the previous cue's trailing lines", () => {
    const vtt =
      "WEBVTT\n\n" +
      "00:00:01.000 --> 00:00:03.000\nthe titanium frame is\nlighter this year\n\n" +
      "00:00:03.000 --> 00:00:05.000\nlighter this year\nby twelve grams\n\n" +
      "00:00:05.000 --> 00:00:07.000\nby twelve grams\nover the last model\n";
    expect(texts(vtt)).toEqual([
      "the titanium frame is lighter this year",
      "by twelve grams",
      "over the last model",
    ]);
  });

  it("keeps only the longest form of a cue that grows word by word", () => {
    // The shape in the reported transcript: one line re-emitted with more
    // appended each time.
    const vtt =
      "WEBVTT\n\n" +
      "00:00:01.000 --> 00:00:03.000\nWe're on Alpe d'Huez, the most\n\n" +
      "00:00:03.000 --> 00:00:05.000\nWe're on Alpe d'Huez, the most famous climb\n\n" +
      "00:00:05.000 --> 00:00:07.000\nWe're on Alpe d'Huez, the most famous climb in cycling\n";
    expect(texts(vtt)).toEqual([
      "We're on Alpe d'Huez, the most famous climb in cycling",
    ]);
  });

  it("drops a cue that is wholly a repeat of the previous one", () => {
    const vtt =
      "WEBVTT\n\n" +
      "00:00:01.000 --> 00:00:03.000\nsame line twice\n\n" +
      "00:00:03.000 --> 00:00:05.000\nsame line twice\n\n" +
      "00:00:05.000 --> 00:00:07.000\nsomething new\n";
    expect(texts(vtt)).toEqual(["same line twice", "something new"]);
  });

  it("keeps a repeat that comes back later, e.g. a chorus", () => {
    const vtt =
      "WEBVTT\n\n" +
      "00:00:01.000 --> 00:00:03.000\nthe chorus repeats here\n\n" +
      "00:00:03.000 --> 00:00:05.000\nsome unrelated verse line\n\n" +
      "00:00:05.000 --> 00:00:07.000\nthe chorus repeats here\n";
    expect(texts(vtt)).toEqual([
      "the chorus repeats here",
      "some unrelated verse line",
      "the chorus repeats here",
    ]);
  });

  it("compares the words left after tags and sound events are stripped", () => {
    // "[Music] I play" then "[Music] I play games" is one growing line once the
    // markers are gone — the collapse only works if stripping happens first.
    const vtt =
      "WEBVTT\n\n" +
      "00:00:01.000 --> 00:00:03.000\n[Music] Hello and <c>welcome</c>\n\n" +
      "00:00:03.000 --> 00:00:05.000\n[Music] Hello and welcome to the show\n";
    expect(texts(vtt)).toEqual(["Hello and welcome to the show"]);
  });

  it("keeps the start time of the cue the text was first seen in", () => {
    const vtt =
      "WEBVTT\n\n" +
      "00:00:10.000 --> 00:00:12.000\nfirst half\n\n" +
      "00:00:12.000 --> 00:00:14.000\nfirst half and second\n";
    expect(parseVtt(vtt)).toEqual([{ ts: 10, text: "first half and second" }]);
  });
});

// These mirror TestParseVTTStripsSpeakerMarkers and TestStripSpeakerMarkers in
// backend/internal/subtitles/vtt_test.go. stripSpeakerMarkers is not exported,
// so it is exercised through parseVtt the same way the sound-event rules are.
describe("parseVtt speaker markers", () => {
  const texts = (vtt: string) => parseVtt(vtt).map((c) => c.text);
  const cue = (text: string) =>
    `WEBVTT\n\n00:00:01.000 --> 00:00:03.000\n${text}\n`;

  it("strips the marker opening a line, spaced or tight", () => {
    expect(texts(cue(">> Good evening and welcome"))).toEqual([
      "Good evening and welcome",
    ]);
    expect(texts(cue(">>Thanks for having me"))).toEqual([
      "Thanks for having me",
    ]);
    expect(texts(cue(">>> And now the news"))).toEqual(["And now the news"]);
  });

  it("decodes the escaped &gt;&gt; spelling before stripping it", () => {
    expect(texts(cue("&gt;&gt; Good evening"))).toEqual(["Good evening"]);
  });

  it("separates two speakers sharing one cue", () => {
    expect(texts(cue("I agree entirely >> So do I"))).toEqual([
      "I agree entirely So do I",
    ]);
    expect(texts(cue(">> >> hello"))).toEqual(["hello"]);
  });

  it("drops a cue that was nothing but a marker", () => {
    const vtt =
      "WEBVTT\n\n" +
      "00:00:01.000 --> 00:00:03.000\n>>>\n\n" +
      "00:00:03.000 --> 00:00:05.000\n>> Real words here\n";
    expect(texts(vtt)).toEqual(["Real words here"]);
  });

  // Shared verbatim with the no-break-space cases in
  // backend/internal/subtitles/vtt_test.go. Go's \s is ASCII-only and this
  // one's is not, so the Go pair names \x{00A0} explicitly to match — without
  // that the panel would drop a marker the summary kept.
  it("treats a no-break space as the boundary, like the Go side", () => {
    // The no-break space itself is then collapsed to a plain one by the
    // sound-event pass, which both sides do \u2014 Go's spaceRe names \x{00A0} too.
    expect(texts(cue("one\u00a0>> two"))).toEqual(["one two"]);
    expect(texts(cue(">>\u00a0Hello"))).toEqual(["Hello"]);
    expect(texts(cue("a >>\u00a0b"))).toEqual(["a b"]);
  });

  // Mirrors TestParseVTTKeepsAnEchoAcrossASpeakerChange. A speaker echoing the
  // words before them is not a rolling window, and the marker is what tells the
  // two apart — so the collapse compares text that still carries it.
  it("keeps an echo across a speaker change", () => {
    expect(
      parseVtt(
        "WEBVTT\n\n00:00:00.000 --> 00:00:03.000\nYeah\n\n" +
          "00:00:03.000 --> 00:00:06.000\n>> Yeah, exactly.\n",
      ),
    ).toEqual([
      { ts: 0, text: "Yeah" },
      { ts: 3, text: "Yeah, exactly." },
    ]);
    expect(
      parseVtt(
        "WEBVTT\n\n00:00:00.000 --> 00:00:03.000\nI think so.\n\n" +
          "00:00:03.000 --> 00:00:06.000\n>> I think so.\n",
      ),
    ).toEqual([
      { ts: 0, text: "I think so." },
      { ts: 3, text: "I think so." },
    ]);
  });

  it("strips a marker wedged tight against a sound event", () => {
    expect(texts(cue("[MUSIC]>> Hello there"))).toEqual(["Hello there"]);
  });

  it("leaves a right-shift operator spoken on screen alone", () => {
    expect(texts(cue("So you write cout>>x to shift it"))).toEqual([
      "So you write cout>>x to shift it",
    ]);
    expect(texts(cue("and >>= does the same in place"))).toEqual([
      "and >>= does the same in place",
    ]);
  });

  it("collapses a rolling window whose lines differ only past the marker", () => {
    const vtt =
      "WEBVTT\n\n" +
      "00:00:00.000 --> 00:00:02.000\n>> I play\n\n" +
      "00:00:02.000 --> 00:00:04.000\n>> I play games\n\n" +
      "00:00:04.000 --> 00:00:06.000\n>> I play games with\n";
    expect(texts(vtt)).toEqual(["I play games with"]);
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
