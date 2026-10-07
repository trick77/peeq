// inDepth.ts reads the in-depth summary's text format (summarize/indepth.go):
// a lead, then sections opened by "### Heading [m:ss]" lines. Plain text rather
// than JSON so a reply cut short still renders every finished section, and a
// reply that ignored the format renders as paragraphs.

export type InDepthSection = {
  heading: string;
  // The stamp as the model wrote it, and in seconds for the seek. Both null
  // when the heading came without one.
  stamp: string | null;
  seconds: number | null;
  body: string[];
};

export type InDepth = { lead: string[]; sections: InDepthSection[] };

// A heading line: "###" and its text. The space after the hashes is optional
// — models drop it often enough to matter.
const HEADING = /^###\s*(.+)$/;

// The stamp, in square or round brackets, anywhere in the heading — models
// move it to the front or glue a period after it.
const STAMP = /[[(](\d{1,2}(?::\d{2}){1,2})[\])]/;

// readHeading splits a heading's text into its words and its stamp, dropping
// the markup models add although the prompt forbids it: bold markers and the
// punctuation left behind once the stamp is cut out.
function readHeading(raw: string): { heading: string; stamp: string | null } {
  const m = STAMP.exec(raw);
  const text = m
    ? raw.slice(0, m.index) + raw.slice(m.index + m[0].length)
    : raw;
  const heading = text
    .replace(/\*\*|__/g, "")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/^[\s.:–—-]+|[\s.:–—-]+$/g, "");
  return { heading, stamp: m ? m[1] : null };
}

function paragraphs(lines: string[]): string[] {
  return lines
    .join("\n")
    .split(/\n\s*\n/)
    .map((p) => p.trim())
    .filter(Boolean);
}

function toSeconds(stamp: string): number {
  return stamp.split(":").reduce((acc, part) => acc * 60 + Number(part), 0);
}

export function parseInDepth(text: string): InDepth {
  const lead: string[] = [];
  const sections: { heading: string; stamp: string | null; lines: string[] }[] =
    [];
  for (const line of text.split("\n")) {
    const m = HEADING.exec(line.trim());
    if (m) {
      sections.push({ ...readHeading(m[1]), lines: [] });
    } else if (sections.length > 0) {
      sections[sections.length - 1].lines.push(line);
    } else {
      lead.push(line);
    }
  }
  return {
    lead: paragraphs(lead),
    sections: sections.map((s) => ({
      heading: s.heading,
      stamp: s.stamp,
      seconds: s.stamp ? toSeconds(s.stamp) : null,
      body: paragraphs(s.lines),
    })),
  };
}

// readMinutes is the closed card's "3 min read": 230 words a minute, rounded
// up, never under one.
export function readMinutes(text: string): number {
  const words = text.split(/\s+/).filter(Boolean).length;
  return Math.max(1, Math.ceil(words / 230));
}
