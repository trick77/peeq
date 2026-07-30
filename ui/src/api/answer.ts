import { streamSSE } from "./stream";

// AnswerSource is one cited passage: enough to render the citation and to open
// the player at the moment it came from.
export type AnswerSource = {
  n: number;
  video_id: string;
  title: string;
  channel_name?: string;
  start_seconds: number;
  kind: string;
};

// AnswerEvent is the narrowed stream.
//
// `sources` always arrives first: the backend finishes retrieval before
// generation starts, so the citation table is already known, and sending it up
// front means a chat failure mid-answer still leaves a usable list of moments.
// `error` replaces the tokens when there is no answer to give — it is not a
// transport failure, and the sources that preceded it are still good.
export type AnswerEvent =
  | { type: "sources"; sources: AnswerSource[] }
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
          const d = data as { sources?: AnswerSource[] };
          onEvent({ type: "sources", sources: d.sources ?? [] });
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
