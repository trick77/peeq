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

const HEADING = /^###\s+(.*?)\s*(?:\[(\d{1,2}(?::\d{2}){1,2})\])?\s*$/;

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
      sections.push({ heading: m[1], stamp: m[2] ?? null, lines: [] });
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
