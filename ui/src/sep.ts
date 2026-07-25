/** The separator between metadata parts in plain-text lines
 *  ("Shared · 3 days left", "@Handle · 2 pending · 2 downloaded").
 *
 *  The spaces are en spaces (U+2002, 0.5em), not plain spaces: the other half
 *  of the UI renders separators as `<span className="dot">·</span>` inside a
 *  flex row whose `gap: 6px` does the spacing (`.card .by`, `.playmeta .by`,
 *  `.chan-handle`). A plain space is only ~0.26em, so text lines read visibly
 *  tighter than the card and player eyebrows sitting next to them. Change the
 *  text spacing here, not at the call sites. */
export const DOT = "\u2002·\u2002";
