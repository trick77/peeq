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
import type { AnswerEvent } from "../api/answer";
import { streamAnswer } from "../api/answer";

const mockedSearchVideos = vi.mocked(searchVideos);
const mockedStreamAnswer = vi.mocked(streamAnswer);

// The query box is matched by role rather than by placeholder text: the
// placeholder is mode-dependent copy now, and these tests should not break
// every time it is reworded.
const box = () => screen.getByRole("textbox");

function toFind() {
  fireEvent.click(screen.getByRole("button", { name: "Find" }));
}

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

    toFind();
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
    toFind();
    submit("platypus");
    expect(await screen.findByText("Summary")).toBeInTheDocument();
  });

  it("badges a chapter-kind match", async () => {
    mockedSearchVideos.mockResolvedValue(
      result({ kind: "chapter", snippet: "Electrolytes: the evidence" }),
    );
    render(<Search onOpen={vi.fn()} />);
    toFind();
    submit("electrolytes");
    expect(await screen.findByText("Chapter")).toBeInTheDocument();
  });

  it("labels a transcript match and shows no raw score", async () => {
    // The distance used to render at the end of every row. It is retrieval
    // diagnostics, not something a reader can act on.
    mockedSearchVideos.mockResolvedValue(result());
    render(<Search onOpen={vi.fn()} />);
    toFind();
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
    toFind();
    submit("electrolytes");

    await screen.findByText("iPhone 27 review");
    const mark = container.querySelector("mark");
    expect(mark).toBeInTheDocument();
    expect(mark).toHaveTextContent("electrolytes");
    // The delimiters themselves must never reach the DOM.
    expect(container.textContent).not.toContain(HIGHLIGHT_START);
    expect(container.textContent).not.toContain(HIGHLIGHT_END);
  });

  it("lands on ask, since a question is what the box is for", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Ask" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    submit("battery life");
    await waitFor(() =>
      expect(mockedSearchVideos).toHaveBeenCalledWith("battery life", "ask"),
    );
  });

  it("searches with find once find is selected", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    toFind();
    submit("battery life");
    await waitFor(() =>
      expect(mockedSearchVideos).toHaveBeenCalledWith("battery life", "find"),
    );
  });

  it("shows FTS operator hints in find mode only", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    expect(screen.queryByText("sodium OR calcium")).not.toBeInTheDocument();

    toFind();
    await waitFor(() =>
      expect(screen.getByText("sodium OR calcium")).toBeInTheDocument(),
    );
  });

  // Find and Ask are tabs, not two settings of one box. Each keeps its own
  // text, so switching cannot reinterpret the other tab's query — and in
  // particular cannot spend a model call on words typed for a keyword search.
  it("keeps a separate query per mode and does not search on a switch", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);

    fireEvent.change(box(), { target: { value: "a whole question?" } });
    toFind();
    expect(box()).toHaveValue("");
    expect(mockedSearchVideos).not.toHaveBeenCalled();

    fireEvent.change(box(), { target: { value: "keyword terms" } });
    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
    expect(box()).toHaveValue("a whole question?");
    expect(mockedSearchVideos).not.toHaveBeenCalled();

    toFind();
    expect(box()).toHaveValue("keyword terms");
  });

  it("keeps each mode's results with its own query", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    render(<Search onOpen={vi.fn()} />);
    toFind();
    submit("iphone");
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
    // Ask has never been searched, so it shows nothing — not Find's results.
    expect(screen.queryByText("iPhone 27 review")).not.toBeInTheDocument();

    toFind();
    expect(screen.getByText("iPhone 27 review")).toBeInTheDocument();
  });

  it("does not search on mount or for a blank query", () => {
    render(<Search onOpen={vi.fn()} />);
    expect(mockedSearchVideos).not.toHaveBeenCalled();
    expect(screen.getByText(/ask peeq anything/i)).toBeInTheDocument();
  });

  // The empty state is an answer, not a dead end with a suggestion attached.
  // The other tab is already one visible click away with its own text.
  it("says find found nothing, and offers no way out", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    toFind();
    submit("electrolytes");

    expect(
      await screen.findByText(/none of your transcripts contain those words/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /instead/i }),
    ).not.toBeInTheDocument();
  });

  it("says the library covers nothing when ask comes up empty", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({ type: "done" });
    });
    render(<Search onOpen={vi.fn()} />);
    submit("unicorn husbandry");
    expect(
      await screen.findByText(/nothing in your library covers that/i),
    ).toBeInTheDocument();
  });

  it("clears stale results when a later search fails", async () => {
    mockedSearchVideos.mockResolvedValueOnce(result());
    render(<Search onOpen={vi.fn()} />);
    toFind();
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
    fireEvent.click(screen.getByRole("button", { name: "Find" }));
    submit("electrolytes");
    await waitFor(() => expect(mockedSearchVideos).toHaveBeenCalled());
    expect(mockedStreamAnswer).not.toHaveBeenCalled();
  });

  // Retrieval returns long before generation does. Showing the moments and the
  // citation list first puts the evidence on screen ahead of the claim that
  // cites it, and pulls the eye off the text being written.
  it("holds the results and sources until the answer settles", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    let emit: ((e: AnswerEvent) => void) | null = null;
    mockedStreamAnswer.mockImplementation(
      (_q, onEvent) =>
        new Promise(() => {
          emit = onEvent;
        }),
    );
    render(<Search onOpen={vi.fn()} />);
    submit("electrolytes");

    await screen.findByText(/Reading your library/);
    emit!({
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
    emit!({ type: "token", text: "Yes — " });

    // Mid-stream: the answer text is there, its evidence is not.
    await waitFor(() =>
      expect(document.querySelector(".answer-body")?.textContent).toContain(
        "Yes —",
      ),
    );
    expect(screen.queryByText("iPhone 27 review")).not.toBeInTheDocument();
    expect(document.querySelector(".answer-sources")).toBeNull();

    emit!({ type: "done" });
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();
    expect(document.querySelector(".answer-sources")).not.toBeNull();
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
