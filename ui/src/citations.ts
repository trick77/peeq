// citations — resolve the [n] markers in a streamed answer into clickable
// citations, leaving everything else as plain text.

// AnswerPart is one piece of a rendered answer: prose, or a citation to
// source number `n`.
export type AnswerPart =
  { kind: "text"; text: string } | { kind: "cite"; n: number };

// splitCitations turns answer text into parts to render.
//
// Two rules exist because this runs mid-stream, on text that is still arriving:
//
//   - A marker whose number has no matching source stays literal. The model is
//     told to cite only the excerpts it was given, but a hallucinated [9] must
//     render as the characters the model produced rather than as a citation
//     pointing nowhere.
//   - A trailing INCOMPLETE marker is withheld entirely. Text ending in "["
//     or "[1" is a citation the rest of which has not arrived; emitting it
//     would flash a raw bracket on screen and then swallow it a frame later.
//     It reappears as a citation the moment its "]" arrives.
export function splitCitations(text: string, known: Set<number>): AnswerPart[] {
  const parts: AnswerPart[] = [];
  let buf = "";
  let i = 0;

  const flush = () => {
    if (buf) parts.push({ kind: "text", text: buf });
    buf = "";
  };

  while (i < text.length) {
    if (text[i] !== "[") {
      buf += text[i];
      i++;
      continue;
    }
    const close = text.indexOf("]", i);
    if (close === -1) {
      // Unterminated. If nothing but digits follows, it is a marker still
      // arriving — withhold it. Otherwise it is an ordinary bracket.
      const rest = text.slice(i + 1);
      if (/^\d*$/.test(rest)) break;
      buf += text[i];
      i++;
      continue;
    }
    const inner = text.slice(i + 1, close);
    const n = Number(inner);
    if (/^\d+$/.test(inner) && known.has(n)) {
      flush();
      parts.push({ kind: "cite", n });
      i = close + 1;
      continue;
    }
    // Not a marker. Consume only the bracket, not the whole span up to that
    // "]": the "]" may belong to a LATER marker ("[note and [1]"), and
    // swallowing the span would render a real citation as literal text.
    buf += text[i];
    i++;
  }
  flush();
  return parts;
}
