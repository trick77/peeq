// A click that ends a text selection is a selection, not a tap. Every row that
// both SHOWS text and acts on a click needs this guard: dragging across the
// words to copy them releases the pointer inside the element, which fires a
// click the user never meant — a seek to some other point in the video, or a
// card opening under the text they were trying to read.
//
// The rows this guards are <button>s, which WebKit also refuses to let the user
// select at all until `user-select: text` is set on them; the two halves of the
// fix travel together (see `.hl .row` / `.toc .row` in index.css).
//
// The transcript no longer works that way. Its cue row stopped being a button —
// only the timestamp is one — because that override can make ONE control's text
// selectable and no browser extends a selection ACROSS two, so a drag over
// several cues selected nothing at all. The guard still applies to the
// timestamp: a drag that happens to end on it must not seek.
export function isSelectingText(): boolean {
  return !!window.getSelection()?.toString();
}

// seekOnClick builds the click handler every one of those seek rows wants:
// jump to `seconds`, unless the click was the end of a drag across the text.
// Kept here beside the predicate so a row cannot pick up the seek and forget
// the guard.
export function seekOnClick(seek: (seconds: number) => void, seconds: number) {
  return () => {
    if (isSelectingText()) return;
    seek(seconds);
  };
}
