import { useState } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { Search } from "./Search";
import { useSearchState } from "../searchState";
import { HIGHLIGHT_END, HIGHLIGHT_START } from "../highlight";
import { DOT } from "../sep";

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

// Harness is what App does: it owns the engine (useSearchState) and renders the
// view against it. Composing them here rather than faking the hook is what makes
// the persistence test below mean anything — leaving has to unmount Search while
// the state it was rendering stays alive.
function Harness({
  onOpen = vi.fn(),
  onOpenVideo = vi.fn(),
  onOpenChannel = vi.fn(),
}: {
  onOpen?: (id: string, s: number) => void;
  onOpenVideo?: (id: string) => void;
  onOpenChannel?: (id: string) => void;
}) {
  const search = useSearchState();
  const [away, setAway] = useState(false);
  return (
    <>
      {/* Stands in for navigating to a video and back. */}
      <button type="button" onClick={() => setAway((v) => !v)}>
        {away ? "return" : "leave"}
      </button>
      {away ? (
        <p>somewhere else</p>
      ) : (
        <Search
          search={search}
          onOpen={onOpen}
          onOpenVideo={onOpenVideo}
          onOpenChannel={onOpenChannel}
        />
      )}
    </>
  );
}

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
    render(<Harness onOpen={onOpen} />);

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
    render(<Harness onOpen={vi.fn()} />);
    toFind();
    submit("platypus");
    expect(await screen.findByText("Summary")).toBeInTheDocument();
  });

  // A summary hit is not a moment. It matched the description of the whole
  // video, has no timestamp of its own and seeks nowhere, so the header must not
  // promise a point to jump to that does not exist. The video still counts: it
  // WAS found, and its card is sitting right below the header.
  it("keeps a summary hit out of the moment count", async () => {
    mockedSearchVideos.mockResolvedValue(
      result({ kind: "summary", snippet: "the platypus lives here" }),
    );
    render(<Harness onOpen={vi.fn()} />);
    toFind();
    submit("platypus");
    await screen.findByText("iPhone 27 review");
    expect(screen.getByText(/1 video.*0 moments/)).toBeInTheDocument();
  });

  it("counts chapter and transcript hits as moments", async () => {
    mockedSearchVideos.mockResolvedValue([
      {
        video: { id: "v1", title: "iPhone 27 review" } as never,
        matches: [
          {
            start_seconds: 0,
            snippet: "about phones",
            distance: 0.1,
            kind: "summary",
          },
          {
            start_seconds: 60,
            snippet: "the battery",
            distance: 0.2,
            kind: "chapter",
          },
          {
            start_seconds: 560,
            snippet: "the new iPhone",
            distance: 0.3,
            kind: "transcript",
          },
        ],
      },
    ]);
    render(<Harness onOpen={vi.fn()} />);
    toFind();
    submit("iphone");
    await screen.findByText("iPhone 27 review");
    expect(screen.getByText(/1 video.*2 moments/)).toBeInTheDocument();
  });

  // A chapter match carries a timestamp, seeks like a transcript match and
  // reads like one, so the badge said nothing the row did not already. It went
  // the way the "Transcript" badge went; only the summary, which has no
  // timestamp of its own, still earns a word.
  it("leaves a chapter-kind match unlabelled", async () => {
    mockedSearchVideos.mockResolvedValue(
      result({ kind: "chapter", snippet: "Electrolytes: the evidence" }),
    );
    render(<Harness onOpen={vi.fn()} />);
    toFind();
    submit("electrolytes");
    expect(
      await screen.findByText("Electrolytes: the evidence"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Chapter")).not.toBeInTheDocument();
  });

  it("leaves a transcript match unlabelled and shows no raw score", async () => {
    // The distance used to render at the end of every row. It is retrieval
    // diagnostics, not something a reader can act on. The "TRANSCRIPT" label
    // that replaced it went the same way, for a different reason: the row's own
    // timestamp already says what it is.
    mockedSearchVideos.mockResolvedValue(result());
    render(<Harness onOpen={vi.fn()} />);
    toFind();
    submit("iphone");
    expect(await screen.findByText("9:20")).toBeInTheDocument();
    expect(screen.queryByText("Transcript")).not.toBeInTheDocument();
    expect(screen.queryByText(/^0\.\d\d$/)).not.toBeInTheDocument();
  });

  it("marks the matched terms inside a snippet", async () => {
    mockedSearchVideos.mockResolvedValue(
      result({
        snippet: `…replace the ${HIGHLIGHT_START}electrolytes${HIGHLIGHT_END} you lose…`,
      }),
    );
    const { container } = render(<Harness onOpen={vi.fn()} />);
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
    render(<Harness onOpen={vi.fn()} />);
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
    render(<Harness onOpen={vi.fn()} />);
    toFind();
    submit("battery life");
    await waitFor(() =>
      expect(mockedSearchVideos).toHaveBeenCalledWith("battery life", "find"),
    );
  });

  it("shows FTS operator hints in find mode only", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Harness onOpen={vi.fn()} />);
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
    render(<Harness onOpen={vi.fn()} />);

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
    render(<Harness onOpen={vi.fn()} />);
    toFind();
    submit("iphone");
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
    // Ask has never been searched, so it shows nothing — not Find's results.
    expect(screen.queryByText("iPhone 27 review")).not.toBeInTheDocument();

    toFind();
    expect(screen.getByText("iPhone 27 review")).toBeInTheDocument();
  });

  // Submitting is the end of typing. The caret staying in the box after the
  // results arrive reads as if nothing was sent, and on a phone it keeps the
  // keyboard over the answer.
  it("gives up focus when the query is submitted", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    render(<Harness onOpen={vi.fn()} />);
    toFind();

    box().focus();
    expect(box()).toHaveFocus();

    submit("iphone");
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();
    expect(box()).not.toHaveFocus();
  });

  it("does not search on mount or for a blank query", () => {
    render(<Harness onOpen={vi.fn()} />);
    expect(mockedSearchVideos).not.toHaveBeenCalled();
    expect(screen.getByText(/ask peeq about anything/i)).toBeInTheDocument();
  });

  // The empty state is an answer, not a dead end with a suggestion attached.
  // The other tab is already one visible click away with its own text.
  it("says find found nothing, and offers no way out", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Harness onOpen={vi.fn()} />);
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
      onEvent({ type: "sources", sources: [], videos: [], coverage: [] });
      onEvent({ type: "done" });
    });
    render(<Harness onOpen={vi.fn()} />);
    submit("unicorn husbandry");
    expect(
      await screen.findByText(/nothing in your library covers that/i),
    ).toBeInTheDocument();
  });

  it("clears stale results when a later search fails", async () => {
    mockedSearchVideos.mockResolvedValueOnce(result());
    render(<Harness onOpen={vi.fn()} />);
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
    render(<Harness onOpen={vi.fn()} />);
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

  // The reason the engine lives above the view: opening one of the videos a
  // search found used to throw the search away, and finding four promising
  // results and watching two of them is the normal way to use this page.
  it("keeps the query and the results after leaving the view and coming back", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    render(<Harness />);
    toFind();
    submit("iphone");
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "leave" }));
    expect(screen.queryByText("iPhone 27 review")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "return" }));
    expect(screen.getByText("iPhone 27 review")).toBeInTheDocument();
    expect(box()).toHaveValue("iphone");
    // Coming back re-renders what was already fetched; it does not re-fetch.
    expect(mockedSearchVideos).toHaveBeenCalledTimes(1);
  });

  it("clears only the box text with the box's own clear", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    render(<Harness />);
    toFind();
    submit("iphone");
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: "Clear the search box" }),
    );

    expect(box()).toHaveValue("");
    // Retyping over a search is not being done with it.
    expect(screen.getByText("iPhone 27 review")).toBeInTheDocument();
    // Nothing rests next to an empty field.
    expect(
      screen.queryByRole("button", { name: "Clear the search box" }),
    ).not.toBeInTheDocument();
  });

  it("puts the whole search away with Clear results", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    render(<Harness />);
    toFind();
    submit("iphone");
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Clear results" }));

    expect(screen.queryByText("iPhone 27 review")).not.toBeInTheDocument();
    expect(screen.queryByText("Matches")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Clear results" }),
    ).not.toBeInTheDocument();
  });

  // The button that was just clicked disappears with the results it cleared,
  // so focus has to be put somewhere deliberately. The box is the only thing
  // left to act on, and the query is still in it: the caret sits at the end,
  // ready to amend the words rather than retype them.
  it("puts focus back in the box after Clear results", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    render(<Harness />);
    toFind();
    submit("iphone");
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Clear results" }));

    const input = box() as HTMLInputElement;
    expect(input).toHaveFocus();
    expect(input).toHaveValue("iphone");
    expect(input.selectionStart).toBe("iphone".length);
    expect(input.selectionEnd).toBe("iphone".length);
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
      status: "downloaded",
    },
    {
      id: "v2",
      title: "Hydration Protocols",
      channel_id: "c2",
      channel_name: "Huberman",
      duration_seconds: 1800,
      has_thumbnail: true,
      status: "downloaded",
    },
  ];

  it("does not ask for an answer in find mode", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    mockedStreamAnswer.mockReturnValue(new Promise(() => {}));
    render(<Harness onOpen={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Find" }));
    submit("electrolytes");
    await waitFor(() => expect(mockedSearchVideos).toHaveBeenCalled());
    expect(mockedStreamAnswer).not.toHaveBeenCalled();
  });

  // Retrieval returns long before generation does. Showing the moments and the
  // citation list first puts the evidence on screen ahead of the claim that
  // cites it, and pulls the eye off the text being written.
  // The three phases of the wait, driven by the frames that actually mark them.
  // The label has to move on each, because each says something the previous one
  // could not: that the question was understood, and that retrieval succeeded.
  it("walks the label through the phases as the frames arrive", async () => {
    let emit: ((e: AnswerEvent) => void) | null = null;
    mockedStreamAnswer.mockImplementation(
      (_q, onEvent) =>
        new Promise(() => {
          emit = onEvent;
        }),
    );
    render(<Harness onOpen={vi.fn()} />);
    submit("what material about electrolytes do we have");

    // Nothing on the wire yet.
    await screen.findByText(/Understanding your question/);

    // The progress frame: retrieval starting, and the understood query with it.
    emit!({
      type: "progress",
      phase: "retrieving",
      topic: "electrolytes",
      intent: "inventory",
    });
    expect(await screen.findByText(/electrolytes/)).toBeInTheDocument();
    expect(
      screen.queryByText(/Understanding your question/),
    ).not.toBeInTheDocument();

    // Retrieval done, the model now thinking — the long, silent stretch.
    emit!({
      type: "sources",
      sources: askSources,
      videos: askVideos,
      coverage: [],
    });
    expect(
      await screen.findByText(
        new RegExp(`Thinking about ${askVideos.length} video`),
      ),
    ).toBeInTheDocument();

    // The first word retires the label entirely.
    emit!({ type: "token", text: "Yes — " });
    await waitFor(() =>
      expect(screen.queryByText(/^Thinking /)).not.toBeInTheDocument(),
    );
  });

  it("holds the moments and the sources until the answer settles", async () => {
    let emit: ((e: AnswerEvent) => void) | null = null;
    mockedStreamAnswer.mockImplementation(
      (_q, onEvent) =>
        new Promise(() => {
          emit = onEvent;
        }),
    );
    render(<Harness onOpen={vi.fn()} />);
    submit("electrolytes");

    await screen.findByText(/Understanding your question/);
    emit!({
      type: "sources",
      sources: askSources,
      videos: askVideos,
      coverage: [],
    });
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

  // Retrieval reaches far more videos than a six-sentence answer can cite, and
  // the citation list alone made Ask look like it knew less than the search box
  // beside it. These used to be a bulleted text list inside the answer panel;
  // they are a tier of compact cards under the matches now, because they ARE
  // videos of the same kind as the ones above and a text row could not say so.
  describe("also in your library", () => {
    const uncited = {
      id: "v9",
      title: "Cramp Myths, Debunked",
      channel_id: "c9",
      channel_name: "Dr. Becky",
      duration_seconds: 900,
      has_thumbnail: true,
      status: "downloaded",
    };

    // Emits a whole settled answer that cites v1 only, with `coverage` on it.
    function answerWith(coverage: object[], text = "Attia covers it[1].") {
      mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
        onEvent({
          type: "sources",
          sources: askSources,
          videos: askVideos,
          coverage: coverage as never,
        });
        if (text) onEvent({ type: "token", text });
        onEvent({ type: "done" });
      });
    }

    it("shows what retrieval found and the answer did not cite", async () => {
      answerWith([...askVideos, uncited]);
      render(<Harness onOpen={vi.fn()} />);
      submit("electrolytes");

      expect(
        await screen.findByText("Also in your library"),
      ).toBeInTheDocument();
      // The answer cited [1] only, so v2 is retrieved-but-uncited too and joins
      // the fourth video down here.
      const tier = document.querySelectorAll(".result.compact");
      expect(tier).toHaveLength(2);
      const titles = [...tier].map(
        (c) => c.querySelector(".rtitle")!.textContent,
      );
      expect(titles).toEqual(["Hydration Protocols", "Cramp Myths, Debunked"]);
      // v1 WAS cited, so it is a full card above and must not repeat down here.
      expect(titles).not.toContain("Why Athletes Cramp");
    });

    // The whole point of the move: these are real cards, with the poster and the
    // byline that let a reader recognise a video, not a line of text.
    it("renders them as cards, compact", async () => {
      answerWith([...askVideos, uncited]);
      render(<Harness onOpen={vi.fn()} />);
      submit("electrolytes");

      const card = await waitFor(() => {
        const c = document.querySelector(".result.compact");
        expect(c).not.toBeNull();
        return c!;
      });
      expect(card.querySelector(".rthumb")).not.toBeNull();
      expect(card.querySelector(".rtitle")).not.toBeNull();
      // No moments were matched in it, so it has no moment rows to show.
      expect(card.querySelectorAll(".match")).toHaveLength(0);
    });

    // The counts above say what the ANSWER stood on. coverageMaxVideos is 20 and
    // an answer typically cites two or three, so folding these in would overstate
    // that header by an order of magnitude. This tier carries its own number.
    it("stays out of the matches count", async () => {
      answerWith([...askVideos, uncited]);
      render(<Harness onOpen={vi.fn()} />);
      submit("electrolytes");

      await screen.findByText("Also in your library");
      expect(document.querySelector(".results-head .n")?.textContent).toContain(
        "1 video",
      );
      // Leading DOT, the same separator the matches header puts between its own
      // counts — see sep.ts, which owns the spacing around it.
      expect(document.querySelector(".also-head .n")?.textContent).toBe(
        `${DOT}2 videos`,
      );
    });

    it("shows nothing when every retrieved video was cited", async () => {
      answerWith(askVideos, "Attia[1] and Huberman[2] both cover it.");
      render(<Harness onOpen={vi.fn()} />);
      submit("electrolytes");

      await screen.findByText("Matches");
      expect(
        screen.queryByText("Also in your library"),
      ).not.toBeInTheDocument();
    });

    // Same settled-answer gate the citations have: a list of what was NOT used,
    // above a half-written answer, describes an answer that does not exist yet.
    it("stays hidden while the answer streams", async () => {
      let emit: ((e: AnswerEvent) => void) | null = null;
      mockedStreamAnswer.mockImplementation(
        (_q, onEvent) =>
          new Promise(() => {
            emit = onEvent;
          }),
      );
      render(<Harness onOpen={vi.fn()} />);
      submit("electrolytes");
      await screen.findByText(/Understanding your question/);

      emit!({
        type: "sources",
        sources: askSources,
        videos: askVideos,
        coverage: [...askVideos, uncited] as never,
      });
      emit!({ type: "token", text: "Attia covers it[1]." });
      await waitFor(() =>
        expect(document.querySelector(".answer-body")?.textContent).toContain(
          "Attia",
        ),
      );
      expect(
        screen.queryByText("Also in your library"),
      ).not.toBeInTheDocument();

      emit!({ type: "done" });
      expect(
        await screen.findByText("Also in your library"),
      ).toBeInTheDocument();
    });

    // The case the move could have lost: an answer that cites nothing renders no
    // matches block at all, and these videos are then the only thing left on the
    // page saying retrieval found anything. They must still show.
    it("shows even when the answer cited nothing", async () => {
      answerWith([...askVideos, uncited], "Nothing specific comes to mind.");
      render(<Harness onOpen={vi.fn()} />);
      submit("electrolytes");

      expect(
        await screen.findByText("Also in your library"),
      ).toBeInTheDocument();
      expect(screen.queryByText("Matches")).not.toBeInTheDocument();
      // Every retrieved video is uncited now, so all three are down here.
      expect(document.querySelectorAll(".result.compact")).toHaveLength(3);
    });

    // The way out of a search lives in the matches header — which is the very
    // thing missing when the answer cited nothing. Without a control here the
    // reader is left with up to coverageMaxVideos cards and no way to put them
    // away, and it clears the whole page rather than just this tier.
    it("offers a way out when it is the only block on the page", async () => {
      answerWith([...askVideos, uncited], "Nothing specific comes to mind.");
      render(<Harness onOpen={vi.fn()} />);
      submit("electrolytes");
      await screen.findByText("Also in your library");
      expect(screen.queryByText("Matches")).not.toBeInTheDocument();

      const clear = screen.getByRole("button", { name: "Clear results" });
      fireEvent.click(clear);
      await waitFor(() =>
        expect(
          screen.queryByText("Also in your library"),
        ).not.toBeInTheDocument(),
      );
      expect(document.querySelectorAll(".result")).toHaveLength(0);
    });

    // ...and exactly one of them on the ordinary page, where the matches header
    // already carries it.
    it("leaves the clear control to the matches header when there is one", async () => {
      answerWith([...askVideos, uncited]);
      render(<Harness onOpen={vi.fn()} />);
      submit("electrolytes");

      await screen.findByText("Also in your library");
      expect(screen.getByText("Matches")).toBeInTheDocument();
      expect(
        screen.getAllByRole("button", { name: "Clear results" }),
      ).toHaveLength(1);
    });

    // It opens the video rather than seeking. A retrieved chunk does sit behind
    // each card, but the model never vouched for it, so a timestamp would assert
    // more than is known.
    it("opens the video without seeking", async () => {
      const onOpenVideo = vi.fn();
      const onOpen = vi.fn();
      answerWith([...askVideos, uncited]);
      render(<Harness onOpen={onOpen} onOpenVideo={onOpenVideo} />);
      submit("electrolytes");

      fireEvent.click(await screen.findByText("Cramp Myths, Debunked"));
      expect(onOpenVideo).toHaveBeenCalledWith("v9");
      expect(onOpen).not.toHaveBeenCalled();
    });

    it("opens the channel from the byline", async () => {
      const onOpenChannel = vi.fn();
      answerWith([...askVideos, uncited]);
      render(<Harness onOpen={vi.fn()} onOpenChannel={onOpenChannel} />);
      submit("electrolytes");

      fireEvent.click(await screen.findByText("Dr. Becky"));
      expect(onOpenChannel).toHaveBeenCalledWith("c9");
    });
  });

  it("streams tokens into the panel and links a citation", async () => {
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({
        type: "sources",
        sources: askSources,
        videos: askVideos,
        coverage: [],
      });
      onEvent({ type: "token", text: "Yes — Attia covers it[1]." });
      onEvent({ type: "done" });
    });
    const onOpen = vi.fn();
    render(<Harness onOpen={onOpen} />);
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
      onEvent({
        type: "sources",
        sources: askSources,
        videos: askVideos,
        coverage: [],
      });
      onEvent({ type: "token", text: "Attia covers it[1]." });
      onEvent({ type: "done" });
    });
    render(<Harness onOpen={vi.fn()} />);
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
    render(<Harness onOpen={vi.fn()} />);
    submit("electrolytes");

    await screen.findByText(/Understanding your question/);
    expect(screen.queryByText("Searching")).not.toBeInTheDocument();

    emit!({
      type: "sources",
      sources: askSources,
      videos: askVideos,
      coverage: [],
    });
    emit!({ type: "token", text: "Yes — " });
    await waitFor(() =>
      expect(
        screen.queryByText(/Understanding your question/),
      ).not.toBeInTheDocument(),
    );
    expect(screen.queryByText("Searching")).not.toBeInTheDocument();
  });

  // Leaving Ask aborts the generation, and streamAnswer RESOLVES on abort
  // rather than rejecting. Settling only on a done frame left the tab loading
  // for good: come back and it is still spinning over an answer nobody is
  // writing.
  // Leaving Ask mid-generation no longer throws the answer away. The model call
  // is already paid for, so it runs to completion and is there when the reader
  // comes back — where it used to be a paragraph frozen mid-word, or nothing.
  it("finishes an answer the reader left mid-generation", async () => {
    let emit: (e: AnswerEvent) => void = () => {};
    let finish: () => void = () => {};
    mockedStreamAnswer.mockImplementation(
      (_q, onEvent) =>
        new Promise((resolve) => {
          emit = onEvent;
          finish = resolve;
          onEvent({
            type: "sources",
            sources: askSources,
            videos: askVideos,
            coverage: [],
          });
        }),
    );
    mockedSearchVideos.mockResolvedValue([]);
    render(<Harness onOpen={vi.fn()} />);
    submit("electrolytes");
    await screen.findByText(/Thinking about \d+ videos?/);

    // Off to a video and back, which unmounts the view entirely.
    fireEvent.click(screen.getByRole("button", { name: "leave" }));
    expect(screen.getByText("somewhere else")).toBeInTheDocument();

    // The stream was not aborted, so it keeps arriving while nobody is looking.
    emit({ type: "token", text: "Yes, twice[1]." });
    emit({ type: "done" });
    finish();

    fireEvent.click(screen.getByRole("button", { name: "return" }));
    await waitFor(() =>
      expect(document.querySelector(".answer-body")?.textContent).toContain(
        "Yes, twice",
      ),
    );
    expect(
      screen.queryByText(/Understanding your question/),
    ).not.toBeInTheDocument();
  });

  it("shows no moments when the answer names none", async () => {
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({
        type: "sources",
        sources: askSources,
        videos: askVideos,
        coverage: [],
      });
      onEvent({ type: "token", text: "The excerpts don't say." });
      onEvent({ type: "done" });
    });
    render(<Harness onOpen={vi.fn()} />);
    submit("electrolytes");

    await screen.findByText(/didn't point at any particular moment/);
    expect(screen.queryByText("Why Athletes Cramp")).not.toBeInTheDocument();
    expect(screen.queryByText("Matches")).not.toBeInTheDocument();
  });

  // The model is down. There are no moments to show — nothing cited them — but
  // the page still has to say what happened rather than going quiet.
  it("says the answer failed when the model is unavailable", async () => {
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({
        type: "sources",
        sources: askSources,
        videos: askVideos,
        coverage: [],
      });
      onEvent({ type: "error", message: "answer unavailable" });
      onEvent({ type: "done" });
    });
    render(<Harness onOpen={vi.fn()} />);
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
    render(<Harness onOpen={vi.fn()} />);
    toAsk();
    submit("electrolytes");

    await waitFor(() =>
      expect(
        screen.queryByText(/Understanding your question/),
      ).not.toBeInTheDocument(),
    );
    expect(screen.queryByText("Matches")).not.toBeInTheDocument();
    // ...and it does not go quiet either. Ask makes one request now, so nothing
    // else on the page would report this: with the line swallowed, a broken
    // stream left the whole view below the box blank and enter looked inert.
    expect(await screen.findByText(/stream failed: 503/)).toBeInTheDocument();
  });

  // Truncated is more use than blank.
  it("keeps partial text when the stream breaks mid-answer", async () => {
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({ type: "sources", sources: [], videos: [], coverage: [] });
      onEvent({ type: "token", text: "Yes — Attia" });
      throw new Error("connection lost");
    });
    render(<Harness onOpen={vi.fn()} />);
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
      onEvent({ type: "sources", sources: [], videos: [], coverage: [] });
      onEvent({ type: "token", text: "First answer." });
      onEvent({ type: "done" });
    });
    render(<Harness onOpen={vi.fn()} />);
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

  // Searching in Find must not quietly bin the answer Ask is still writing.
  // Nothing aborts that stream any more, so a shared run ticket would leave the
  // model call running to completion and then throw its answer away — paying
  // for it twice over when the reader switches back and has to ask again.
  it("keeps an Ask answer that finished while Find was being used", async () => {
    let emit: (e: AnswerEvent) => void = () => {};
    let finish: () => void = () => {};
    mockedStreamAnswer.mockImplementation(
      (_q, onEvent) =>
        new Promise((resolve) => {
          emit = onEvent;
          finish = resolve;
          onEvent({
            type: "sources",
            sources: askSources,
            videos: askVideos,
            coverage: [],
          });
        }),
    );
    mockedSearchVideos.mockResolvedValue([]);
    render(<Harness onOpen={vi.fn()} />);

    toAsk();
    submit("electrolytes");
    await screen.findByText(/Thinking about \d+ videos?/);

    // Off to the other tab, and a keyword search there.
    fireEvent.click(screen.getByRole("button", { name: "Find" }));
    submit("iphone");
    await waitFor(() => expect(mockedSearchVideos).toHaveBeenCalled());

    // The answer lands while Find is on screen.
    emit({ type: "token", text: "Yes, twice[1]." });
    emit({ type: "done" });
    finish();

    toAsk();
    await waitFor(() =>
      expect(document.querySelector(".answer-body")?.textContent).toContain(
        "Yes, twice",
      ),
    );
  });

  // The two tabs keep their own everything, and that has to include being
  // cleared: putting Find's results away must not touch what Ask found.
  it("clears one tab's results and leaves the other tab's alone", async () => {
    mockedStreamAnswer.mockImplementation(async (_q, onEvent) => {
      onEvent({
        type: "sources",
        sources: askSources,
        videos: askVideos,
        coverage: [],
      });
      onEvent({ type: "token", text: "Yes[1]." });
      onEvent({ type: "done" });
    });
    mockedSearchVideos.mockResolvedValue([
      {
        video: { id: "v9", title: "iPhone 27 review" } as never,
        matches: [
          {
            start_seconds: 560,
            snippet: "the new iPhone",
            distance: 0.1,
            kind: "transcript",
          },
        ],
      },
    ]);
    render(<Harness onOpen={vi.fn()} />);

    toAsk();
    submit("electrolytes");
    await waitFor(() =>
      expect(document.querySelector(".answer-body")?.textContent).toContain(
        "Yes",
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: "Find" }));
    submit("iphone");
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clear results" }));
    expect(screen.queryByText("iPhone 27 review")).not.toBeInTheDocument();

    toAsk();
    expect(document.querySelector(".answer-body")?.textContent).toContain(
      "Yes",
    );
  });
});

describe("Search — the caret on arrival", () => {
  beforeEach(() => {
    mockedSearchVideos.mockReset();
    mockedStreamAnswer.mockReset();
    mockedStreamAnswer.mockReturnValue(new Promise(() => {}));
  });

  // Arriving on the view, the box is the only thing there is to do.
  it("takes the caret while the box is empty", async () => {
    render(<Harness />);

    // waitFor, not a bare assertion: React 19 runs passive effects after paint,
    // so the box existing does not mean the focus effect has run yet.
    await waitFor(() => expect(document.activeElement).toBe(box()));
  });

  it("leaves focus alone when returning to a query already in the box", async () => {
    mockedSearchVideos.mockResolvedValue(result());
    render(<Harness />);

    toFind();
    submit("iphone");
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();

    // Away and back. The query and its results survive the trip, so the box is
    // not empty on the way in — focusing it would scroll to the top and, on a
    // phone, raise the keyboard over the results being returned to.
    fireEvent.click(screen.getByRole("button", { name: "leave" }));
    fireEvent.click(screen.getByRole("button", { name: "return" }));

    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();
    expect(box()).toHaveValue("iphone");
    await waitFor(() => expect(document.activeElement).not.toBe(box()));
  });
});
