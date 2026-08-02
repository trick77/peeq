// captions — put the on-video cues back in the middle of the picture.
//
// Peeq positions no captions of its own. The player and the share page both
// hand a <track kind="subtitles"> to a native <video> and the browser draws the
// cues, so whatever placement the WebVTT asks for is what shows up.
//
// What the WebVTT asks for is the left edge. We fetch YouTube's auto-generated
// caption track (--write-auto-subs), and its cues carry `align:start
// position:0%` on essentially every line — see the real fixture in
// backend/internal/subtitles/vtt_test.go. Neither VTT parser strips those
// settings (both simply ignore them) and the backend serves the stored text
// byte-for-byte on purpose, so they arrive intact and the browser honours them.
// The result is captions glued to the bottom-left corner.
//
// The fix belongs here rather than in the data. Stripping the settings at
// ingest would only help videos downloaded afterwards, and stripping them at
// serve would break the verbatim contract the transcript panel and the
// user-facing .vtt download both depend on — the stored VTT is the only source
// a tombstoned video has left. Resetting the cues in the browser touches
// nothing durable and fixes every video already in the library.
//
// CSS cannot do this: ::cue accepts colour, background and font properties,
// never placement.

// centerCues resets every cue's placement to the WebVTT defaults, which is what
// puts the text centred along the bottom of the picture.
//
// Only VTTCue carries the three fields; the TextTrackCue base class does not,
// which is why each cue is checked rather than cast.
export function centerCues(track: TextTrack | null | undefined): void {
  const cues = track?.cues;
  if (!cues) return;
  for (const cue of Array.from(cues)) {
    if (!("align" in cue)) continue;
    const vtt = cue as VTTCue;
    vtt.align = "center";
    vtt.position = "auto";
    vtt.line = "auto";
  }
}

// centerCuesRef is the callback ref both <track> elements use.
//
// The cues do not exist when the element mounts: track.cues stays null until
// the browser loads the file, which it only does once the track's mode leaves
// "disabled". So the work hangs off the element's own load event, which fires
// whenever that happens — on mount for a viewer with captions on by default,
// and later at the moment they press CC if they did not. readyState 2 is
// HTMLTrackElement.LOADED, covering a track that finished before the ref ran.
//
// React 19 runs the returned function when the element goes away.
export function centerCuesRef(
  el: HTMLTrackElement | null,
): (() => void) | void {
  if (!el) return;
  const onLoad = () => centerCues(el.track);
  el.addEventListener("load", onLoad);
  if (el.readyState === 2) onLoad();
  return () => el.removeEventListener("load", onLoad);
}
