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
        state={state({ status: "streaming", text: "Yes — twice, and" })}
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
        state={state({ status: "streaming", text: "" })}
        onOpen={vi.fn()}
      />,
    );
    expect(screen.getByText(/Reading your library/)).toBeInTheDocument();

    rerender(
      <Panel
        state={state({ status: "streaming", text: "Yes — " })}
        onOpen={vi.fn()}
      />,
    );
    expect(screen.queryByText(/Reading your library/)).not.toBeInTheDocument();
  });

  it("counts the sources the answer cited, not the ones retrieved", () => {
    render(
      <Panel
        state={state({ text: "Attia says so[1], and again[3]." })}
        onOpen={vi.fn()}
      />,
    );
    expect(screen.queryByText(/Reading your library/)).not.toBeInTheDocument();
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
        state={state({ status: "streaming", text: "partial" })}
        onOpen={vi.fn()}
      />,
    );
    const body = document.querySelector(".answer-body");
    expect(body).toHaveAttribute("aria-live", "polite");
    expect(body).toHaveAttribute("aria-busy", "true");
  });
});
