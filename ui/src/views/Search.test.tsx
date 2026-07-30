import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { Search } from "./Search";
import { HIGHLIGHT_END, HIGHLIGHT_START } from "../highlight";

vi.mock("../api/search", () => ({
  searchVideos: vi.fn(),
}));
vi.mock("../api/answer", () => ({
  streamAnswer: vi.fn(),
}));

import { searchVideos } from "../api/search";
import { streamAnswer } from "../api/answer";

const mockedSearchVideos = vi.mocked(searchVideos);
const mockedStreamAnswer = vi.mocked(streamAnswer);

// The query box is matched by role rather than by placeholder text: the
// placeholder is mode-dependent copy now, and these tests should not break
// every time it is reworded.
const box = () => screen.getByRole("textbox");

function submit(q: string) {
  fireEvent.change(box(), { target: { value: q } });
  fireEvent.submit(screen.getByRole("search"));
}

function result(over: Partial<{ kind: string; snippet: string }> = {}) {
  return [
    {
      video: { id: "v1", title: "iPhone 27 review" } as never,
      matches: [
        {
          start_seconds: 560,
          snippet: over.snippet ?? "the new iPhone",
          distance: 0.1,
          kind: over.kind ?? "transcript",
        },
      ],
    },
  ];
}

describe("Search", () => {
  beforeEach(() => {
    mockedSearchVideos.mockReset();
    mockedStreamAnswer.mockReset();
    // Default: a stream that never emits, so Find-mode tests are unaffected.
    mockedStreamAnswer.mockReturnValue(new Promise(() => {}));
  });

  it("shows results and navigates to a moment", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    const onOpen = vi.fn();
    render(<Search onOpen={onOpen} />);

    submit("iphone");

    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();
    fireEvent.click(screen.getByText(/the new iPhone/));
    expect(onOpen).toHaveBeenCalledWith("v1", 560);
  });

  it("badges a summary-kind match", async () => {
    mockedSearchVideos.mockResolvedValue(
      result({ kind: "summary", snippet: "the platypus lives here" }),
    );
    render(<Search onOpen={vi.fn()} />);
    submit("platypus");
    expect(await screen.findByText("Summary")).toBeInTheDocument();
  });

  it("badges a chapter-kind match", async () => {
    mockedSearchVideos.mockResolvedValue(
      result({ kind: "chapter", snippet: "Electrolytes: the evidence" }),
    );
    render(<Search onOpen={vi.fn()} />);
    submit("electrolytes");
    expect(await screen.findByText("Chapter")).toBeInTheDocument();
  });

  it("labels a transcript match and shows no raw score", async () => {
    // The distance used to render at the end of every row. It is retrieval
    // diagnostics, not something a reader can act on.
    mockedSearchVideos.mockResolvedValue(result());
    render(<Search onOpen={vi.fn()} />);
    submit("iphone");
    expect(await screen.findByText("Transcript")).toBeInTheDocument();
    expect(screen.queryByText(/^0\.\d\d$/)).not.toBeInTheDocument();
  });

  it("marks the matched terms inside a snippet", async () => {
    mockedSearchVideos.mockResolvedValue(
      result({
        snippet: `…replace the ${HIGHLIGHT_START}electrolytes${HIGHLIGHT_END} you lose…`,
      }),
    );
    const { container } = render(<Search onOpen={vi.fn()} />);
    submit("electrolytes");

    await screen.findByText("iPhone 27 review");
    const mark = container.querySelector("mark");
    expect(mark).toBeInTheDocument();
    expect(mark).toHaveTextContent("electrolytes");
    // The delimiters themselves must never reach the DOM.
    expect(container.textContent).not.toContain(HIGHLIGHT_START);
    expect(container.textContent).not.toContain(HIGHLIGHT_END);
  });

  it("defaults to find mode and searches with it", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Find" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    submit("battery life");
    await waitFor(() =>
      expect(mockedSearchVideos).toHaveBeenCalledWith("battery life", "find"),
    );
  });

  it("shows FTS operator hints in find mode only", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    expect(screen.getByText("sodium OR calcium")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
    await waitFor(() =>
      expect(screen.queryByText("sodium OR calcium")).not.toBeInTheDocument(),
    );
  });

  it("re-runs the current query when the mode is switched", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    submit("electrolytes");
    await waitFor(() =>
      expect(mockedSearchVideos).toHaveBeenCalledWith("electrolytes", "find"),
    );

    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
    // Same words, other mode — no retype.
    await waitFor(() =>
      expect(mockedSearchVideos).toHaveBeenCalledWith("electrolytes", "ask"),
    );
  });

  it("does not search on mount or for a blank query", () => {
    render(<Search onOpen={vi.fn()} />);
    expect(mockedSearchVideos).not.toHaveBeenCalled();
    expect(screen.getByText(/find the exact words/i)).toBeInTheDocument();
  });

  it("offers the other mode when find comes up empty", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    submit("electrolytes");

    expect(
      await screen.findByText(/none of your transcripts contain those words/i),
    ).toBeInTheDocument();
    const offer = screen.getByRole("button", { name: /by meaning instead/i });
    expect(offer).toBeInTheDocument();

    fireEvent.click(offer);
    await waitFor(() =>
      expect(mockedSearchVideos).toHaveBeenCalledWith("electrolytes", "ask"),
    );
  });

  it("says the library covers nothing when ask comes up empty", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
    submit("unicorn husbandry");
    expect(
      await screen.findByText(/nothing in your library covers that/i),
    ).toBeInTheDocument();
  });

  it("clears stale results when a later search fails", async () => {
    mockedSearchVideos.mockResolvedValueOnce(result());
    render(<Search onOpen={vi.fn()} />);
    submit("iphone");
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();

    mockedSearchVideos.mockRejectedValueOnce(new Error("search backend down"));
    submit("battery");

    expect(await screen.findByText(/search backend down/i)).toBeInTheDocument();
    expect(screen.queryByText("iPhone 27 review")).not.toBeInTheDocument();
  });

  it("retires the error line when the box is emptied", async () => {
    mockedSearchVideos.mockRejectedValueOnce(new Error("search backend down"));
    render(<Search onOpen={vi.fn()} />);
    submit("iphone");
    expect(await screen.findByText(/search backend down/i)).toBeInTheDocument();

    // The error belongs to a query that is no longer on screen.
    submit("");
    await waitFor(() =>
      expect(
        screen.queryByText(/search backend down/i),
      ).not.toBeInTheDocument(),
    );
  });
});

describe("Search — the Ask answer", () => {
  beforeEach(() => {
    mockedSearchVideos.mockReset();
    mockedStreamAnswer.mockReset();
  });

  function toAsk() {
    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
  }

  it("does not ask for an answer in find mode", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    mockedStreamAnswer.mockReturnValue(new Promise(() => {}));
    render(<Search onOpen={vi.fn()} />);
    submit("electrolytes");
    await waitFor(() => expect(mockedSearchVideos).toHaveBeenCalled());
    expect(mockedStreamAnswer).not.toHaveBeenCalled();
  });

  // The whole point of two requests: retrieval returns in a moment and
  // generation takes seconds, so the moments must be on screen while the
  // answer is still being written.
  it("renders results while the answer is still streaming", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    mockedStreamAnswer.mockReturnValue(new Promise(() => {}));
    render(<Search onOpen={vi.fn()} />);
    toAsk();
    submit("electrolytes");

    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();
    expect(screen.getByText(/Reading your library/)).toBeInTheDocument();
  });

  it("streams tokens into the panel and links a citation", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({
        type: "sources",
        sources: [
          {
            n: 1,
            video_id: "v1",
            title: "Why Athletes Cramp",
            start_seconds: 872,
            kind: "transcript",
          },
        ],
      });
      onEvent({ type: "token", text: "Yes — Attia covers it[1]." });
      onEvent({ type: "done" });
    });
    const onOpen = vi.fn();
    render(<Search onOpen={onOpen} />);
    toAsk();
    submit("electrolytes");

    const cite = await screen.findByRole("button", {
      name: /Source 1: Why Athletes Cramp/,
    });
    fireEvent.click(cite);
    expect(onOpen).toHaveBeenCalledWith("v1", 872);
  });

  // A broken answer stream must never take the results down with it.
  it("drops the panel but keeps results when the stream fails", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    mockedStreamAnswer.mockRejectedValue(new Error("stream failed: 503"));
    render(<Search onOpen={vi.fn()} />);
    toAsk();
    submit("electrolytes");

    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.queryByText(/Reading your library/),
      ).not.toBeInTheDocument(),
    );
    expect(screen.queryByText(/answer unavailable/i)).not.toBeInTheDocument();
    expect(document.querySelector(".errline")).toBeNull();
  });

  // Truncated is more use than blank.
  it("keeps partial text when the stream breaks mid-answer", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({ type: "sources", sources: [] });
      onEvent({ type: "token", text: "Yes — Attia" });
      throw new Error("connection lost");
    });
    render(<Search onOpen={vi.fn()} />);
    toAsk();
    submit("electrolytes");

    await waitFor(() =>
      expect(document.querySelector(".answer-body")?.textContent).toContain(
        "Yes — Attia",
      ),
    );
  });

  it("clears the previous answer when a new search starts", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    mockedStreamAnswer.mockImplementationOnce(async (_q, onEvent) => {
      onEvent({ type: "sources", sources: [] });
      onEvent({ type: "token", text: "First answer." });
      onEvent({ type: "done" });
    });
    render(<Search onOpen={vi.fn()} />);
    toAsk();
    submit("first");
    await waitFor(() =>
      expect(document.querySelector(".answer-body")?.textContent).toContain(
        "First answer.",
      ),
    );

    mockedStreamAnswer.mockReturnValue(new Promise(() => {}));
    submit("second");
    await waitFor(() =>
      expect(document.querySelector(".answer-body")?.textContent).not.toContain(
        "First answer.",
      ),
    );
  });
});
