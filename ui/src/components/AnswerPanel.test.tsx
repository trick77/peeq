import type { ComponentProps } from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { AnswerPanel, type AnswerState } from "./AnswerPanel";

// Four retrieved passages, of which an answer typically uses some. The panel
// shows what it used — see answerSources.ts. Passages 1 and 4 come from the SAME
// video, which is the ordinary case: retrieval hands the model up to three
// passages per video.
const sources = [
  {
    n: 1,
    video_id: "v1",
    title: "Why Athletes Cramp",
    channel_name: "Peter Attia MD",
    start_seconds: 872,
    kind: "transcript",
    snippet: "the electrolytes you replace",
  },
  {
    n: 2,
    video_id: "v2",
    title: "Hydration Protocols",
    channel_name: "Huberman Lab",
    start_seconds: 0,
    kind: "summary",
    snippet: "a whole-video summary",
  },
  {
    n: 3,
    video_id: "v3",
    title: "Sodium and Sport",
    channel_name: "Attia",
    start_seconds: 40,
    kind: "transcript",
    snippet: "sodium losses in sweat",
  },
  {
    n: 4,
    video_id: "v1",
    title: "Why Athletes Cramp",
    channel_name: "Peter Attia MD",
    start_seconds: 1140,
    kind: "transcript",
    snippet: "and the cramp comes back",
  },
];

// The videos frame of the same stream. A source carries channel_name but no
// channel_id, so the panel looks the id up here to make the channel navigate.
const videos = [
  {
    id: "v1",
    title: "Why Athletes Cramp",
    channel_id: "c1",
    channel_name: "Peter Attia MD",
    duration_seconds: 1200,
    has_thumbnail: true,
    status: "downloaded",
  },
  {
    id: "v2",
    title: "Hydration Protocols",
    channel_id: "c2",
    channel_name: "Huberman Lab",
    duration_seconds: 1200,
    has_thumbnail: true,
    status: "downloaded",
  },
  {
    id: "v3",
    title: "Sodium and Sport",
    channel_id: "c3",
    channel_name: "Attia",
    duration_seconds: 1200,
    has_thumbnail: true,
    status: "downloaded",
  },
];

function state(over: Partial<AnswerState> = {}): AnswerState {
  return { status: "done", text: "", sources, videos, ...over };
}

// Panel supplies the handlers every test would otherwise repeat; anything a
// test passes explicitly wins.
function Panel(
  props: Partial<ComponentProps<typeof AnswerPanel>> & { state: AnswerState },
) {
  return (
    <AnswerPanel
      onOpen={vi.fn()}
      onOpenVideo={vi.fn()}
      onOpenChannel={vi.fn()}
      {...props}
    />
  );
}

describe("AnswerPanel", () => {
  it("renders partial text while streaming", () => {
    render(
      <Panel
        state={state({ status: "generating", text: "Yes — twice, and" })}
        onOpen={vi.fn()}
      />,
    );
    // Text is split into fade segments, so match on the container.
    expect(document.querySelector(".answer-body")?.textContent).toContain(
      "Yes — twice, and",
    );
  });

  // The spinner answers "is anything happening"; the arriving words answer it
  // better, so it goes as soon as they do.
  it("shows the spinner only until the first token", () => {
    const { rerender } = render(
      <Panel
        state={state({ status: "generating", text: "" })}
        onOpen={vi.fn()}
      />,
    );
    // Generating with a retrieved set in hand: the label names the count, which
    // is what says retrieval already succeeded.
    expect(screen.getByText(/Thinking about 3 videos/)).toBeInTheDocument();

    rerender(
      <Panel
        state={state({ status: "generating", text: "Yes — " })}
        onOpen={vi.fn()}
      />,
    );
    expect(
      screen.queryByText(/Thinking about 3 videos/),
    ).not.toBeInTheDocument();
  });

  // The wait has three parts and each label is true only of its own. One label
  // across the whole wait could not say that retrieval had already succeeded,
  // which is what made five seconds of the model thinking read like a stall.
  describe("the wait label", () => {
    it("names the phase it is actually in", () => {
      const { rerender } = render(
        <Panel state={state({ status: "understanding", text: "" })} />,
      );
      expect(
        screen.getByText("Understanding your question"),
      ).toBeInTheDocument();

      rerender(<Panel state={state({ status: "retrieving", text: "" })} />);
      expect(screen.getByText("Searching your library")).toBeInTheDocument();

      rerender(<Panel state={state({ status: "generating", text: "" })} />);
      // videos carries the retrieved set, so the count is real.
      expect(
        screen.getByText(`Thinking about ${videos.length} videos`),
      ).toBeInTheDocument();
    });

    // The reader's only view of what was actually searched for. Without it a
    // rewrite that drops the wrong word just returns worse answers, silently.
    it("shows the understood query while retrieving", () => {
      render(
        <Panel
          state={state({
            status: "retrieving",
            text: "",
            topic: "bike geometry",
          })}
        />,
      );
      expect(screen.getByText(/bike geometry/)).toBeInTheDocument();
    });

    // Generating before the videos frame has been seen, or a retrieval that
    // found none: there is no count to name, so the label must not say "0".
    it("names no count it does not have", () => {
      render(
        <Panel
          state={{ status: "generating", text: "", sources: [], videos: [] }}
        />,
      );
      expect(screen.getByText("Thinking")).toBeInTheDocument();
    });

    // Retrieval is well under a second; generating is around five. A topic shown
    // only while retrieving would flash past, so the guard against a silent bad
    // rewrite has to survive into the phase the reader actually sits through.
    it("keeps the understood query up through the long phase", () => {
      render(
        <Panel
          state={state({
            status: "generating",
            text: "",
            topic: "bike geometry",
          })}
        />,
      );
      // The count still leads, so "retrieval succeeded" is what survives if the
      // label has to ellipsize.
      expect(
        screen.getByText(
          `Thinking about ${videos.length} videos on “bike geometry”`,
        ),
      ).toBeInTheDocument();
    });

    it("falls back to the library when nothing was rewritten", () => {
      render(
        <Panel state={state({ status: "retrieving", text: "", topic: "" })} />,
      );
      expect(screen.getByText("Searching your library")).toBeInTheDocument();
    });

    // The spinner is the first child of a right-anchored block, so a label that
    // changes length moves it. The reserve stops that — and is dropped once the
    // answer settles, so "N sources" keeps sitting against the right edge.
    it("reserves the label width only while waiting", () => {
      const { rerender } = render(
        <Panel state={state({ status: "retrieving", text: "" })} />,
      );
      expect(document.querySelector(".answer .hd .status")).toHaveClass(
        "waiting",
      );

      rerender(<Panel state={state({ status: "done", text: "Yes.[1]" })} />);
      expect(document.querySelector(".answer .hd .status")).not.toHaveClass(
        "waiting",
      );
    });

    // A phase label is about the wait BEFORE the first word; once words arrive
    // they say it better.
    it("goes as soon as text arrives, whatever the phase", () => {
      render(<Panel state={state({ status: "generating", text: "Yes — " })} />);
      expect(
        screen.queryByText(/Thinking|Searching|Understanding/),
      ).not.toBeInTheDocument();
    });
  });

  it("counts the sources the answer cited, not the ones retrieved", () => {
    render(
      <Panel
        state={state({ text: "Attia says so[1], and again[3]." })}
        onOpen={vi.fn()}
      />,
    );
    expect(screen.queryByText(/Thinking/)).not.toBeInTheDocument();
    // Four were retrieved; two videos were used.
    expect(screen.getByText("2 sources")).toBeInTheDocument();
  });

  // The renumbering signal. The answer cites [3] first, so it renders as 1 —
  // and the accessible name has to agree with the numeral, or a screen-reader
  // user is sent to a row that is not there.
  it("renumbers citations from 1 in order of first mention", () => {
    const onOpen = vi.fn();
    render(
      <Panel
        state={state({ text: "Sodium matters[3], and so does water[1]." })}
        onOpen={onOpen}
      />,
    );
    const first = screen.getByRole("button", { name: /Source 1: Sodium and/ });
    expect(first).toHaveTextContent("1");
    expect(
      screen.queryByRole("button", { name: /Source 3:/ }),
    ).not.toBeInTheDocument();

    fireEvent.click(first);
    expect(onOpen).toHaveBeenCalledWith("v3", 40);
  });

  // A row stands for the whole video — it may cover several cited moments — so
  // it opens the video where the viewer left it rather than seeking to whichever
  // moment happened to be cited first.
  it("lists the cited sources as resting rows that open the video on click", () => {
    const onOpen = vi.fn();
    const onOpenVideo = vi.fn();
    render(
      <Panel
        state={state({ text: "One[1] and two[2]." })}
        onOpen={onOpen}
        onOpenVideo={onOpenVideo}
      />,
    );
    const list = document.querySelector(".answer-sources") as HTMLElement;
    const rows = list.querySelectorAll(".srcrow");
    expect(rows).toHaveLength(2);
    fireEvent.click(rows[0]);
    expect(onOpenVideo).toHaveBeenCalledWith("v1");
    expect(onOpen).not.toHaveBeenCalled();
  });

  // Passages 1 and 4 are two moments of the same video. It is ONE source: one
  // row, one numeral — however many of its passages the answer leaned on.
  it("lists a video cited twice as a single source", () => {
    render(
      <Panel
        state={state({ text: "Early on[1], and later[4]; also[3]." })}
        onOpen={vi.fn()}
      />,
    );
    const list = document.querySelector(".answer-sources") as HTMLElement;
    expect(list.querySelectorAll(".srcrow")).toHaveLength(2);
    expect(within(list).getAllByText("Why Athletes Cramp")).toHaveLength(1);
    expect(screen.getByText("2 sources")).toBeInTheDocument();
  });

  // Both marks say 1 because both are that video. They are still two different
  // controls seeking to two different places, which only their accessible names
  // can say — so the moment stays in the name even though the row drops it.
  it("gives two moments of one video the same numeral and their own seeks", () => {
    const onOpen = vi.fn();
    render(
      <Panel
        state={state({ text: "Early on[1], and later[4]." })}
        onOpen={onOpen}
      />,
    );
    const marks = document.querySelectorAll(".answer-body .cite");
    expect(marks).toHaveLength(2);
    expect(marks[0]).toHaveTextContent("1");
    expect(marks[1]).toHaveTextContent("1");
    expect(marks[1]).toHaveAccessibleName(
      "Source 1: Why Athletes Cramp at 19:00",
    );

    fireEvent.click(marks[1]);
    expect(onOpen).toHaveBeenCalledWith("v1", 1140);
  });

  // A mark against a comma or a full stop drops its own gap: the punctuation
  // already leaves one, and both together read as a stray space.
  it("closes a mark up against the comma or full stop it follows", () => {
    render(<Panel state={state({ text: "Early on[1], and later[4]." })} />);
    const marks = document.querySelectorAll(".answer-body .cite");
    expect(marks[0]).toHaveClass("tight");
    expect(marks[1]).toHaveClass("tight");
  });

  // After a word there is nothing to close up against, so the mark keeps the gap
  // that separates the numeral from the letters.
  it("keeps the gap on a mark that follows a word", () => {
    render(<Panel state={state({ text: "Early on[1] and later." })} />);
    const mark = document.querySelector(".answer-body .cite")!;
    expect(mark).not.toHaveClass("tight");
  });

  // The channel name navigates, and it is a SIBLING of the moment button rather
  // than a child: a button inside a button is invalid markup no browser agrees
  // on, and clicking the channel must not also open the video.
  it("navigates to the channel from a source row without opening the video", () => {
    const onOpen = vi.fn();
    const onOpenVideo = vi.fn();
    const onOpenChannel = vi.fn();
    render(
      <Panel
        state={state({ text: "One[1]." })}
        onOpen={onOpen}
        onOpenVideo={onOpenVideo}
        onOpenChannel={onOpenChannel}
      />,
    );

    const line = document.querySelector(
      ".answer-sources .srcline",
    ) as HTMLElement;
    expect(line.querySelector(".srcrow .chan-link")).toBeNull();

    fireEvent.click(
      within(line).getByRole("button", { name: "Peter Attia MD" }),
    );
    expect(onOpenChannel).toHaveBeenCalledWith("c1");
    expect(onOpen).not.toHaveBeenCalled();
    expect(onOpenVideo).not.toHaveBeenCalled();
  });

  // Nothing to navigate to without an id: the name still shows, as plain text.
  it("leaves the channel name inert when the videos frame is missing", () => {
    render(<Panel state={state({ text: "One[1].", videos: undefined })} />);
    const line = document.querySelector(
      ".answer-sources .srcline",
    ) as HTMLElement;
    expect(line.querySelector(".chan-link")).toBeNull();
    expect(within(line).getByText("Peter Attia MD")).toBeInTheDocument();
  });

  it("leaves an uncited passage out of the sources list", () => {
    render(<Panel state={state({ text: "Only one[1]." })} onOpen={vi.fn()} />);
    const list = document.querySelector(".answer-sources") as HTMLElement;
    expect(list.querySelectorAll(".srcrow")).toHaveLength(1);
    expect(screen.queryByText("Sodium and Sport")).not.toBeInTheDocument();
  });

  // No row carries a timestamp — not the transcript passages, and not the em
  // dash the summary row used to stand in with. A row that can cover several
  // moments has no one moment to print, and one printed beside the title reads
  // like the only one.
  it("shows no timestamp on any source row", () => {
    render(
      <Panel
        state={state({ text: "One[1], two[2], later[4]." })}
        onOpen={vi.fn()}
      />,
    );
    const list = document.querySelector(".answer-sources") as HTMLElement;
    expect(list.querySelector(".ts")).toBeNull();
    expect(list.textContent).not.toMatch(/\d+:\d\d|—/);
  });

  // Nothing was cited, so there is nothing below either — say why rather than
  // ending on a bare paragraph.
  it("says so when the answer names no moment", () => {
    render(
      <Panel state={state({ text: "I couldn't tell." })} onOpen={vi.fn()} />,
    );
    expect(
      screen.getByText(/didn't point at any particular moment/),
    ).toBeInTheDocument();
    expect(document.querySelectorAll(".srcrow")).toHaveLength(0);
  });

  it("reports a failed answer without listing uncited passages", () => {
    render(
      <Panel state={state({ text: "", failed: true })} onOpen={vi.fn()} />,
    );
    expect(screen.getByText(/Couldn't write an answer/)).toBeInTheDocument();
    expect(document.querySelectorAll(".srcrow")).toHaveLength(0);
  });

  // Truncated is more use than blank.
  it("keeps partial text when the answer failed mid-stream", () => {
    render(
      <Panel
        state={state({ text: "Yes — Attia[1]", failed: true })}
        onOpen={vi.fn()}
      />,
    );
    expect(document.querySelector(".answer-body")?.textContent).toContain(
      "Yes — Attia",
    );
    expect(document.querySelectorAll(".srcrow")).toHaveLength(1);
    expect(
      screen.queryByText(/Couldn't write an answer/),
    ).not.toBeInTheDocument();
  });

  it("renders nothing when there is no answer and no sources", () => {
    const { container } = render(
      <Panel
        state={{ status: "done", text: "", sources: [] }}
        onOpen={vi.fn()}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("announces the settled answer politely rather than every token", () => {
    render(
      <Panel
        state={state({ status: "generating", text: "partial" })}
        onOpen={vi.fn()}
      />,
    );
    const body = document.querySelector(".answer-body");
    expect(body).toHaveAttribute("aria-live", "polite");
    expect(body).toHaveAttribute("aria-busy", "true");
  });
});

// The coverage list moved OUT of this panel: it is a tier of compact video cards
// under the matches now, not a bulleted text list in here. See Search.test.tsx
// for what it does there. What is asserted here is only that the panel no longer
// draws it — a state carrying coverage must produce no list of its own.
describe("retrieved but uncited videos", () => {
  const uncited = {
    id: "v4",
    title: "Cramp Myths, Debunked",
    channel_id: "c4",
    channel_name: "Dr. Becky",
    duration_seconds: 900,
    has_thumbnail: true,
    status: "downloaded",
  };

  it("are not rendered by the panel", () => {
    render(
      <Panel
        state={state({
          text: "Only the first one matters.[1]",
          coverage: [...videos, uncited],
        })}
      />,
    );
    expect(screen.queryByText(/Also in your library/)).not.toBeInTheDocument();
    expect(screen.queryByText("Cramp Myths, Debunked")).not.toBeInTheDocument();
    // The citations it DOES own are untouched by the move.
    expect(screen.getByText("Sources")).toBeInTheDocument();
  });
});
