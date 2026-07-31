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
};

// AnswerEvent is the narrowed stream.
//
// `sources` always arrives first: the backend finishes retrieval before
// generation starts, so the citation table is already known, and sending it up
// front means a chat failure mid-answer still leaves a usable list of moments.
// `error` replaces the tokens when there is no answer to give — it is not a
// transport failure, and the sources that preceded it are still good.
export type AnswerEvent =
  | { type: "sources"; sources: AnswerSource[]; videos: AnswerVideo[] }
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
        case "sources": {
          const d = data as {
            sources?: AnswerSource[];
            videos?: AnswerVideo[];
          };
          onEvent({
            type: "sources",
            sources: d.sources ?? [],
            videos: d.videos ?? [],
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
