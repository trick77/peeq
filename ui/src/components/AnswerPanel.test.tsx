import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { AnswerPanel, type AnswerState } from "./AnswerPanel";

const sources = [
  {
    n: 1,
    video_id: "v1",
    title: "Why Athletes Cramp",
    channel_name: "Peter Attia MD",
    start_seconds: 872,
    kind: "transcript",
  },
  {
    n: 2,
    video_id: "v2",
    title: "Hydration Protocols",
    channel_name: "Huberman Lab",
    start_seconds: 0,
    kind: "summary",
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
    expect(screen.getByText(/Reading your library/)).toBeInTheDocument();
    // Text is split into fade segments, so match on the container.
    expect(document.querySelector(".answer-body")?.textContent).toContain(
      "Yes — twice, and",
    );
  });

  it("drops the spinner and counts sources when done", () => {
    render(<AnswerPanel state={state({ text: "Done." })} onOpen={vi.fn()} />);
    expect(screen.queryByText(/Reading your library/)).not.toBeInTheDocument();
    expect(screen.getByText("2 sources")).toBeInTheDocument();
  });

  it("renders an inline citation that rests visible and jumps on click", () => {
    const onOpen = vi.fn();
    render(
      <AnswerPanel
        state={state({ text: "Attia says so[1]." })}
        onOpen={onOpen}
      />,
    );
    // At rest, with no hover: the numeral is in the document already.
    const cite = screen.getByRole("button", { name: /Source 1: Why Athletes/ });
    expect(cite).toBeInTheDocument();
    fireEvent.click(cite);
    expect(onOpen).toHaveBeenCalledWith("v1", 872);
  });

  it("lists every source as a resting row that jumps on click", () => {
    const onOpen = vi.fn();
    render(<AnswerPanel state={state({ text: "x" })} onOpen={onOpen} />);
    const list = document.querySelector(".answer-sources") as HTMLElement;
    const rows = within(list).getAllByRole("button");
    expect(rows).toHaveLength(2);
    fireEvent.click(rows[1]);
    expect(onOpen).toHaveBeenCalledWith("v2", 0);
  });

  it("shows a summary source without a timestamp", () => {
    render(<AnswerPanel state={state({ text: "x" })} onOpen={vi.fn()} />);
    const list = document.querySelector(".answer-sources") as HTMLElement;
    expect(within(list).getByText("—")).toBeInTheDocument();
  });

  // A failed answer must not blank the panel: the sources that preceded the
  // failure are still good.
  it("keeps the sources when the answer failed with no text", () => {
    render(
      <AnswerPanel
        state={state({ text: "", failed: true })}
        onOpen={vi.fn()}
      />,
    );
    expect(
      screen.getByText(/moments below are still good/),
    ).toBeInTheDocument();
    expect(document.querySelectorAll(".srcrow")).toHaveLength(2);
  });

  // Truncated is more use than blank.
  it("keeps partial text when the answer failed mid-stream", () => {
    render(
      <AnswerPanel
        state={state({ text: "Yes — Attia", failed: true })}
        onOpen={vi.fn()}
      />,
    );
    expect(document.querySelector(".answer-body")?.textContent).toContain(
      "Yes — Attia",
    );
    expect(
      screen.queryByText(/moments below are still good/),
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
