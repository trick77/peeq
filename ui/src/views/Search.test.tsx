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

  // Ask makes ONE request. Its moments are the ones the answer cited, which
  // the answer stream already carries, so a second /api/search would spend
  // another embedding call to build a list this view no longer shows.
  it("lands on ask and spends a single request on it", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Ask" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    submit("battery life");
    await waitFor(() => expect(mockedStreamAnswer).toHaveBeenCalled());
    expect(mockedSearchVideos).not.toHaveBeenCalled();
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
    expect(screen.getByText(/ask peeq about anything/i)).toBeInTheDocument();
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
    // The backend always reports retrieval first, even when it found nothing —
    // which is what tells the view "the library covers nothing" apart from
    // "the request never got that far".
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({ type: "sources", sources: [], videos: [] });
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
    toFind();
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

  // The evidence Ask renders: two retrieved passages from two videos. What the
  // answer cites out of these is what the page shows.
  const askSources = [
    {
      n: 1,
      video_id: "v1",
      title: "Why Athletes Cramp",
      start_seconds: 872,
      kind: "transcript",
      snippet: "the electrolytes you replace",
    },
    {
      n: 2,
      video_id: "v2",
      title: "Hydration Protocols",
      start_seconds: 60,
      kind: "transcript",
      snippet: "how much sodium to drink",
    },
  ];
  const askVideos = [
    {
      id: "v1",
      title: "Why Athletes Cramp",
      channel_id: "c1",
      channel_name: "Attia",
      duration_seconds: 3600,
      has_thumbnail: true,
    },
    {
      id: "v2",
      title: "Hydration Protocols",
      channel_id: "c2",
      channel_name: "Huberman",
      duration_seconds: 1800,
      has_thumbnail: true,
    },
  ];

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
  it("holds the moments and the sources until the answer settles", async () => {
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
    emit!({ type: "sources", sources: askSources, videos: askVideos });
    emit!({ type: "token", text: "Yes — Attia covers it[1]." });

    // Mid-stream: the answer text is there, its evidence is not.
    await waitFor(() =>
      expect(document.querySelector(".answer-body")?.textContent).toContain(
        "Yes —",
      ),
    );
    expect(screen.queryByText("Why Athletes Cramp")).not.toBeInTheDocument();
    expect(document.querySelector(".answer-sources")).toBeNull();

    emit!({ type: "done" });
    // The title now appears twice — once as a source row, once as a card.
    expect(await screen.findAllByText("Why Athletes Cramp")).toHaveLength(2);
    expect(document.querySelector(".answer-sources")).not.toBeNull();
  });

  it("streams tokens into the panel and links a citation", async () => {
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({ type: "sources", sources: askSources, videos: askVideos });
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

  // The reported bug: retrieval hands the model twelve passages, it uses six,
  // and the page listed all twelve — videos that never mentioned the topic,
  // under an answer whose first citation was [2].
  it("shows only the moments the answer cited", async () => {
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({ type: "sources", sources: askSources, videos: askVideos });
      onEvent({ type: "token", text: "Attia covers it[1]." });
      onEvent({ type: "done" });
    });
    render(<Search onOpen={vi.fn()} />);
    submit("electrolytes");

    await screen.findAllByText("Why Athletes Cramp");
    expect(screen.queryByText("Hydration Protocols")).not.toBeInTheDocument();
    expect(screen.getByText(/1 video/)).toBeInTheDocument();
  });

  // Ask has one wait and one indicator for it. The page-level "Searching" line
  // outlives retrieval now that Ask stays loading until the answer settles, so
  // it would sit next to the panel's spinner for the whole generation.
  it("never stacks a second spinner beside the answer", async () => {
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
    expect(screen.queryByText("Searching")).not.toBeInTheDocument();

    emit!({ type: "sources", sources: askSources, videos: askVideos });
    emit!({ type: "token", text: "Yes — " });
    await waitFor(() =>
      expect(
        screen.queryByText(/Reading your library/),
      ).not.toBeInTheDocument(),
    );
    expect(screen.queryByText("Searching")).not.toBeInTheDocument();
  });

  // Leaving Ask aborts the generation, and streamAnswer RESOLVES on abort
  // rather than rejecting. Settling only on a done frame left the tab loading
  // for good: come back and it is still spinning over an answer nobody is
  // writing.
  it("leaves no spinner behind when the tab is left mid-answer", async () => {
    mockedStreamAnswer.mockImplementation(
      (_q, onEvent, signal) =>
        new Promise((resolve) => {
          onEvent({ type: "sources", sources: askSources, videos: askVideos });
          signal?.addEventListener("abort", () => resolve());
        }),
    );
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    submit("electrolytes");
    await screen.findByText(/Reading your library/);

    fireEvent.click(screen.getByRole("button", { name: "Find" }));
    toAsk();

    await waitFor(() =>
      expect(
        screen.queryByText(/Reading your library/),
      ).not.toBeInTheDocument(),
    );
    expect(screen.queryByText("Searching")).not.toBeInTheDocument();
  });

  it("shows no moments when the answer names none", async () => {
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({ type: "sources", sources: askSources, videos: askVideos });
      onEvent({ type: "token", text: "The excerpts don't say." });
      onEvent({ type: "done" });
    });
    render(<Search onOpen={vi.fn()} />);
    submit("electrolytes");

    await screen.findByText(/didn't point at any particular moment/);
    expect(screen.queryByText("Why Athletes Cramp")).not.toBeInTheDocument();
    expect(screen.queryByText("Matches")).not.toBeInTheDocument();
  });

  // The model is down. There are no moments to show — nothing cited them — but
  // the page still has to say what happened rather than going quiet.
  it("says the answer failed when the model is unavailable", async () => {
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({ type: "sources", sources: askSources, videos: askVideos });
      onEvent({ type: "error", message: "answer unavailable" });
      onEvent({ type: "done" });
    });
    render(<Search onOpen={vi.fn()} />);
    submit("electrolytes");

    expect(
      await screen.findByText(/Couldn't write an answer/),
    ).toBeInTheDocument();
    expect(screen.queryByText("Why Athletes Cramp")).not.toBeInTheDocument();
    expect(screen.queryByText("Matches")).not.toBeInTheDocument();
  });

  // A failed answer has cited nothing, so there is nothing to stand behind. The
  // page says the answer failed and stops there rather than falling back to the
  // whole retrieved set.
  it("shows no moments when the stream fails outright", async () => {
    mockedStreamAnswer.mockRejectedValue(new Error("stream failed: 503"));
    render(<Search onOpen={vi.fn()} />);
    toAsk();
    submit("electrolytes");

    await waitFor(() =>
      expect(
        screen.queryByText(/Reading your library/),
      ).not.toBeInTheDocument(),
    );
    expect(screen.queryByText("Matches")).not.toBeInTheDocument();
    expect(document.querySelector(".errline")).toBeNull();
  });

  // Truncated is more use than blank.
  it("keeps partial text when the stream breaks mid-answer", async () => {
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({ type: "sources", sources: [], videos: [] });
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
      onEvent({ type: "sources", sources: [], videos: [] });
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
