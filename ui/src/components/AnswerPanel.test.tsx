import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { AnswerPanel, type AnswerState } from "./AnswerPanel";

// Three retrieved passages, of which an answer typically uses some. The panel
// shows what it used — see answerSources.ts.
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
];

function state(over: Partial<AnswerState> = {}): AnswerState {
  return { status: "done", text: "", sources, ...over };
}

describe("AnswerPanel", () => {
  it("renders partial text while streaming", () => {
    render(
      <AnswerPanel
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
      <AnswerPanel
        state={state({ status: "streaming", text: "" })}
        onOpen={vi.fn()}
      />,
    );
    expect(screen.getByText(/Reading your library/)).toBeInTheDocument();

    rerender(
      <AnswerPanel
        state={state({ status: "streaming", text: "Yes — " })}
        onOpen={vi.fn()}
      />,
    );
    expect(screen.queryByText(/Reading your library/)).not.toBeInTheDocument();
  });

  it("counts the sources the answer cited, not the ones retrieved", () => {
    render(
      <AnswerPanel
        state={state({ text: "Attia says so[1], and again[3]." })}
        onOpen={vi.fn()}
      />,
    );
    expect(screen.queryByText(/Reading your library/)).not.toBeInTheDocument();
    // Three were retrieved; two were used.
    expect(screen.getByText("2 sources")).toBeInTheDocument();
  });

  // The renumbering signal. The answer cites [3] first, so it renders as 1 —
  // and the accessible name has to agree with the numeral, or a screen-reader
  // user is sent to a row that is not there.
  it("renumbers citations from 1 in order of first mention", () => {
    const onOpen = vi.fn();
    render(
      <AnswerPanel
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

  it("lists the cited sources as resting rows that jump on click", () => {
    const onOpen = vi.fn();
    render(
      <AnswerPanel
        state={state({ text: "One[1] and two[2]." })}
        onOpen={onOpen}
      />,
    );
    const list = document.querySelector(".answer-sources") as HTMLElement;
    const rows = within(list).getAllByRole("button");
    expect(rows).toHaveLength(2);
    fireEvent.click(rows[1]);
    expect(onOpen).toHaveBeenCalledWith("v2", 0);
  });

  it("leaves an uncited passage out of the sources list", () => {
    render(
      <AnswerPanel state={state({ text: "Only one[1]." })} onOpen={vi.fn()} />,
    );
    const list = document.querySelector(".answer-sources") as HTMLElement;
    expect(within(list).getAllByRole("button")).toHaveLength(1);
    expect(screen.queryByText("Sodium and Sport")).not.toBeInTheDocument();
  });

  it("shows a summary source without a timestamp", () => {
    render(<AnswerPanel state={state({ text: "x[2]" })} onOpen={vi.fn()} />);
    const list = document.querySelector(".answer-sources") as HTMLElement;
    expect(within(list).getByText("—")).toBeInTheDocument();
  });

  // Nothing was cited, so there is nothing below either — say why rather than
  // ending on a bare paragraph.
  it("says so when the answer names no moment", () => {
    render(
      <AnswerPanel
        state={state({ text: "I couldn't tell." })}
        onOpen={vi.fn()}
      />,
    );
    expect(
      screen.getByText(/didn't point at any particular moment/),
    ).toBeInTheDocument();
    expect(document.querySelectorAll(".srcrow")).toHaveLength(0);
  });

  it("reports a failed answer without listing uncited passages", () => {
    render(
      <AnswerPanel
        state={state({ text: "", failed: true })}
        onOpen={vi.fn()}
      />,
    );
    expect(screen.getByText(/Couldn't write an answer/)).toBeInTheDocument();
    expect(document.querySelectorAll(".srcrow")).toHaveLength(0);
  });

  // Truncated is more use than blank.
  it("keeps partial text when the answer failed mid-stream", () => {
    render(
      <AnswerPanel
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
      <AnswerPanel
        state={{ status: "done", text: "", sources: [] }}
        onOpen={vi.fn()}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("announces the settled answer politely rather than every token", () => {
    render(
      <AnswerPanel
        state={state({ status: "streaming", text: "partial" })}
        onOpen={vi.fn()}
      />,
    );
    const body = document.querySelector(".answer-body");
    expect(body).toHaveAttribute("aria-live", "polite");
    expect(body).toHaveAttribute("aria-busy", "true");
  });
});
