import { streamSSE } from "./stream";

// AnswerSource is one retrieved passage: enough to render the citation, list it
// as a source, show it as a moment, and open the player at it.
//
// `snippet` may contain HIGHLIGHT_START/HIGHLIGHT_END around the matched terms,
// exactly like a search match — render it through splitHighlights.
export type AnswerSource = {
  n: number;
  video_id: string;
  title: string;
  channel_name?: string;
  start_seconds: number;
  kind: string;
  snippet: string;
};

// AnswerVideo is the video a source belongs to, in the shape a result card
// reads. It travels once per video rather than once per source, and is a
// structural subset of api/types Video, so one card component serves both Find's
// full records and Ask's narrow ones.
export type AnswerVideo = {
  id: string;
  title: string;
  channel_id: string;
  channel_name: string;
  duration_seconds: number;
  has_thumbnail: boolean;
  thumbnail_version?: string;
  // status distinguishes a cited video peeq holds from one it only read: see
  // ResultCardVideo, which this has to keep satisfying.
  status: string;
  // published_at is the air date the card's byline shows. Optional the same way
  // Video's is: the backend omits a date it never learned.
  published_at?: string;
};

// LibraryCount answers "how many", for a question that asked it. It is computed
// in SQL under the constraints the question named, so it is authoritative in a
// way the cited excerpts are not — they are a sample of at most twelve passages.
export type LibraryCount = {
  videos: number;
  duration_seconds: number;
  channels: number;
};

// AnswerEvent is the narrowed stream.
//
// `sources` always arrives first: the backend finishes retrieval before
// generation starts, so the citation table is already known, and sending it up
// front means a chat failure mid-answer still leaves a usable list of moments.
// `error` replaces the tokens when there is no answer to give — it is not a
// transport failure, and the sources that preceded it are still good.
export type AnswerEvent =
  | {
      // Retrieval is starting. It arrives BEFORE `sources`, once the backend has
      // worked out what the question is about — a step that costs a second or so
      // and used to pass with nothing on the wire, so the panel claimed the
      // library was being searched before searching had begun.
      //
      // `topic` is the question with its framing stripped: "bike geometry" from
      // "what material about bike geometry do we have". Empty when there was
      // nothing to strip, when the step failed, or when it is not configured —
      // all of which mean the raw question was searched, never an error. Showing
      // it is what makes a bad rewrite visible instead of silent.
      type: "progress";
      phase: "retrieving";
      topic: string;
      // Whether the reader asked HOW MANY videos there are, rather than what any
      // of them say. It replaced an "intent" label that sorted questions into
      // "what the library holds" and "what the videos say" — a distinction the
      // backend dropped, because Ask only ever answers from this library and
      // sorting questions into two modes implied one of them was a general
      // explanation of the subject.
      counting: boolean;
      // The constraints the question named and the search actually applied, in
      // the reader's own words: "unwatched", "Veritasium", "Science & Research".
      // Shown for the same reason `topic` is, and with more at stake — a rewrite
      // makes the answer worse, a filter makes videos disappear.
      filters?: string[];
      // Channel names the question used that the library has nothing under. The
      // constraint was dropped and the search widened; the panel says so.
      unresolvedChannels?: string[];
    }
  | {
      type: "sources";
      sources: AnswerSource[];
      videos: AnswerVideo[];
      filters?: string[];
      unresolvedChannels?: string[];
      // Constraints that found nothing and were dropped so the search could
      // return something. Non-empty means what follows is the WHOLE library, not
      // the slice that was asked for.
      relaxed?: string[];
      // Present only for an inventory question ("how many …").
      counts?: LibraryCount;
      // Every video retrieval found, best-ranked first, one entry each — not just
      // the ones that won an excerpt slot. Search subtracts what the answer cited
      // to get the "Also in your library" tier, and that has to happen on the
      // CLIENT rather than on the server: this frame is sent before generation, so
      // at that point nothing knows what will be cited.
      coverage: AnswerVideo[];
    }
  | { type: "token"; text: string }
  | { type: "done" }
  | { type: "error"; message: string };

// streamAnswer opens the answer stream and narrows each frame, ignoring frames
// it does not recognise. Resolves when the server closes the stream or the
// signal aborts; rejects only on a transport or auth failure, since every
// answer-level problem arrives as an `error` event instead.
export async function streamAnswer(
  q: string,
  onEvent: (e: AnswerEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  await streamSSE(
    `/api/search/answer?q=${encodeURIComponent(q)}`,
    ({ event, data }) => {
      switch (event) {
        case "progress": {
          const d = data as {
            topic?: string;
            counting?: boolean;
            filters?: string[];
            unresolved_channels?: string[];
          };
          onEvent({
            type: "progress",
            phase: "retrieving",
            topic: d.topic ?? "",
            counting: d.counting ?? false,
            filters: d.filters ?? [],
            unresolvedChannels: d.unresolved_channels ?? [],
          });
          break;
        }
        case "sources": {
          const d = data as {
            sources?: AnswerSource[];
            videos?: AnswerVideo[];
            coverage?: AnswerVideo[];
            filters?: string[];
            relaxed?: string[];
            unresolved_channels?: string[];
            counts?: LibraryCount;
          };
          onEvent({
            type: "sources",
            sources: d.sources ?? [],
            videos: d.videos ?? [],
            coverage: d.coverage ?? [],
            filters: d.filters ?? [],
            relaxed: d.relaxed ?? [],
            unresolvedChannels: d.unresolved_channels ?? [],
            counts: d.counts,
          });
          break;
        }
        case "token": {
          const d = data as { text?: string };
          if (typeof d.text === "string") {
            onEvent({ type: "token", text: d.text });
          }
          break;
        }
        case "done":
          onEvent({ type: "done" });
          break;
        case "error": {
          const d = data as { error?: string };
          onEvent({ type: "error", message: d.error ?? "answer unavailable" });
          break;
        }
      }
    },
    signal,
  );
}
