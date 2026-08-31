import { useEffect, useMemo, useRef, useState } from "react";
import { Icon } from "../icons";
import { formatDuration } from "../format";
import { seekOnClick } from "../selection";
import {
  parseVtt,
  matchesFind,
  highlightCue,
  transcriptToText,
  useCopyTranscript,
  type Cue,
} from "../vtt";

// TranscriptCard is the collapsible Transcript panel: a find box, the .txt /
// .vtt / Copy row, and the click-to-seek cue list.
//
// It exists because three pages now want it — the Player, the public share
// page, and the page for a video peeq has read but not downloaded — and the
// first two had it written out by hand, identically, in two files. A third copy
// would have made the drift a certainty rather than a risk; the two WebVTT
// parsers this app already keeps in lockstep (see vtt.ts) are enough of that.
//
// It takes a URL and imports nothing from api/. That is a hard constraint, not
// a stylistic one: the share page renders for an unauthenticated visitor and
// deliberately pulls in no session-gated code, so a component that fetched
// through the API client could not be used there at all. The caller decides
// which URL the VTT comes from — token-gated, session-gated, either way.
export function TranscriptCard({
  vttUrl,
  filenameBase,
  seek,
  className = "full",
  defaultOpen = false,
}: {
  // vttUrl is where the WebVTT is fetched from, lazily, the first time the
  // panel is opened. Nothing is loaded until then.
  vttUrl: string;
  // filenameBase names both downloads (it gets ".txt" / ".vtt" appended). Built
  // from the title alone by vtt.ts's transcriptFilenameBase — never from the
  // video id, which IS the YouTube id, nor from a share token, which is a
  // secret. Neither belongs in a recipient's Downloads folder.
  filenameBase: string;
  // seek jumps the player to a cue. Omitted when there is nothing to jump —
  // a video peeq has read but not downloaded has no media — and the cue rows
  // then render as plain text rather than as buttons that would do nothing.
  seek?: (seconds: number) => void;
  // className distinguishes the Player's full-width card from the share page's,
  // which carries its own width and spacing rules.
  className?: string;
  // defaultOpen starts the panel expanded, for the one caller whose page has
  // nothing else on it: a video with captions but no summary is opened FROM a
  // button that says "Read transcript", and landing on a collapsed accordion
  // makes that a promise kept only after a second click. Everywhere else the
  // transcript is the long tail below a summary and stays folded away.
  //
  // Read once, as the initial state: the panel is the user's to fold after
  // that, and a later render must not spring it back open.
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  // loaded is the URL `parsed` came from, so the cache below can tell "already
  // have this transcript" from "have A, now being asked for B".
  const [loaded, setLoaded] = useState<string | null>(null);
  const [parsed, setParsed] = useState<Cue[]>([]);
  const [find, setFind] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The copy button's own state, kept apart from `error` so a failed copy never
  // looks like a failed load.
  const { copied, error: copyError, copy } = useCopyTranscript();

  // cues is what the panel actually shows: the parsed cues, but ONLY while they
  // still belong to the URL being asked for.
  //
  // Derived rather than stored, because the alternative is a frame of lying.
  // The share page keeps this component mounted across a token change, so a
  // plain `cues` array would go on rendering the previous share's transcript
  // until the new fetch landed — one video's words under another video's title.
  // Clearing in an effect would still leave that frame; deriving removes it.
  // Memoized rather than merely derived: the match list and the scroll effect
  // below take `cues` as a dependency, and a bare `[]` literal on the
  // not-yet-loaded path is a new array every render — enough to reset the
  // search cursor on a loop.
  const cues = useMemo(
    () => (loaded === vttUrl ? parsed : []),
    [loaded, vttUrl, parsed],
  );

  // Fetch and parse on first open, and again only if the URL changes. Reopening
  // a panel whose transcript is already loaded costs nothing.
  useEffect(() => {
    if (!open || loaded === vttUrl) return;
    let active = true;
    setLoading(true);
    setError(null);
    fetch(vttUrl)
      .then((res) => {
        if (!res.ok) throw new Error("failed to load transcript");
        return res.text();
      })
      .then((text) => {
        if (!active) return;
        setParsed(parseVtt(text));
        setLoaded(vttUrl);
      })
      .catch(() => {
        if (active) setError("Failed to load transcript.");
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [open, vttUrl, loaded]);

  // matches is the position of every cue holding the term, in document order —
  // the list the counter counts, the steppers walk and the scroll aims at.
  //
  // One entry per CUE, not per occurrence: a line saying the word three times
  // is one stop, because the row is what gets scrolled to and marked, and
  // highlightCue already marks all three inside it.
  //
  // Memoized because it is O(cues) and this panel routinely holds five thousand
  // of them. The count it replaces ran matchesFind twice per cue per render, on
  // every keystroke.
  const matches = useMemo(() => {
    if (!find.trim()) return [];
    const out: number[] = [];
    cues.forEach((c, i) => {
      if (matchesFind(c.text, find)) out.push(i);
    });
    return out;
  }, [cues, find]);
  // The same set, for the per-row tint — a Set so drawing five thousand rows
  // does not run a linear scan per row.
  const hits = useMemo(() => new Set(matches), [matches]);

  // at is the match the steppers are parked on. Clamped on READ rather than
  // corrected in an effect: typing another letter can shrink the list under a
  // cursor pointing past its end, and a frame rendering "8 / 3" is a frame of
  // lying. activeCue is that match's index in `cues`, or -1 for none.
  const [at, setAt] = useState(0);
  const activeAt = matches.length ? Math.min(at, matches.length - 1) : -1;
  const activeCue = activeAt < 0 ? -1 : matches[activeAt];

  // A new search starts at its own first match, not wherever the last one left
  // the cursor.
  useEffect(() => {
    setAt(0);
  }, [find, cues]);

  // step walks the matches, wrapping at both ends. Wrapping rather than
  // stopping dead: the counter says which match you are on, so arriving back at
  // 1 of 47 reads as a lap rather than as a stuck button.
  function step(delta: number) {
    if (!matches.length) return;
    setAt((v) => {
      const cur = Math.min(v, matches.length - 1);
      return (cur + delta + matches.length) % matches.length;
    });
  }

  const bodyRef = useRef<HTMLDivElement>(null);

  // Bring the current match to the middle of the cue list.
  //
  // Written against the container's own scrollTop rather than with
  // scrollIntoView, which walks EVERY scrollable ancestor: the cue list is a
  // 340px window part-way down a long player page, and pressing "next" would
  // otherwise scroll the page around the transcript as well as the transcript
  // itself. This moves exactly one box. offsetTop is measured from
  // .transcript-body, which index.css gives `position: relative` for it.
  useEffect(() => {
    if (activeCue < 0) return;
    const body = bodyRef.current;
    const row = body?.querySelector<HTMLElement>(`[data-cue="${activeCue}"]`);
    if (!body || !row) return;
    body.scrollTop =
      row.offsetTop - body.clientHeight / 2 + row.offsetHeight / 2;
  }, [activeCue]);

  // The .txt download is built from the parsed cues rather than fetched, so it
  // contains exactly what the panel shows and exactly what Copy puts on the
  // clipboard — the de-duplicated transcript, not the raw rolling-window VTT.
  function downloadTxt() {
    const blob = new Blob([transcriptToText(cues)], {
      type: "text/plain;charset=utf-8",
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filenameBase + ".txt";
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  }

  return (
    <div className={`card ${className}`}>
      <button
        type="button"
        className="hd hd-btn"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        {/* A leading topic glyph and a trailing chevron, as Contents,
            Highlights and Details all carry. This header used to spend its
            leading slot on a chevronRight that rotated 90deg when open, which
            left the card as the one panel with no icon saying what it is, and
            with its open/shut affordance in a different place from every
            sibling. */}
        <Icon name="captions" size="16px" />
        <span className="lbl">Transcript</span>
        <span className="chev">
          <Icon name={open ? "chevronUp" : "chevronDown"} size="15px" />
        </span>
      </button>
      {open && (
        <>
          <div className="tsearch">
            <div className="searchbox">
              <Icon name="search" size="16px" />
              <input
                placeholder="Find in transcript…"
                value={find}
                onChange={(e) => setFind(e.target.value)}
                // Enter steps forward, Shift+Enter back — the pair every find
                // bar has, and the only way to walk matches without taking a
                // hand off the keyboard. Scoped to the input rather than the
                // window: a global Enter handler would fire from anywhere on
                // the player page, and nothing else here wants the key.
                onKeyDown={(e) => {
                  if (e.key !== "Enter") return;
                  e.preventDefault();
                  step(e.shiftKey ? -1 : 1);
                }}
              />
              {/* Position over total, both counting MATCHES. It used to read
                  matching lines over total lines — "1 / 5413" on a long
                  transcript, which looks like a position in a list of 5413
                  matches and is neither a position nor a count of them. */}
              <span className="count mono">
                {!find.trim()
                  ? "—"
                  : matches.length === 0
                    ? "None"
                    : `${activeAt + 1} / ${matches.length}`}
              </span>
              {/* Only while something is being searched for: with no term
                  there is nothing to step through, and two dead arrows would
                  be furniture. Present and pressable the moment there is —
                  never revealed by hover. */}
              {find.trim() !== "" && (
                <span className="tfind-nav">
                  <button
                    type="button"
                    onClick={() => step(-1)}
                    disabled={matches.length === 0}
                    aria-label="Previous match"
                    title="Previous match"
                  >
                    <Icon name="chevronUp" size="15px" />
                  </button>
                  <button
                    type="button"
                    onClick={() => step(1)}
                    disabled={matches.length === 0}
                    aria-label="Next match"
                    title="Next match"
                  >
                    <Icon name="chevronDown" size="15px" />
                  </button>
                </span>
              )}
            </div>
            {cues.length > 0 && (
              <div className="transcript-dl">
                {/* No "Download" label: both pills already carry the download
                    icon, and the only thing left to say is which format. */}
                <button type="button" className="pill" onClick={downloadTxt}>
                  <Icon name="download" size="14px" /> .txt
                </button>
                {/* The captions, not the video: the same VTT the panel above
                    parsed, named from the title alone. */}
                <a
                  className="pill"
                  href={vttUrl}
                  download={filenameBase + ".vtt"}
                >
                  <Icon name="download" size="14px" /> .vtt
                </a>
                <button
                  type="button"
                  className="pill transcript-copy"
                  onClick={() => copy(cues)}
                >
                  <Icon name={copied ? "check" : "copy"} size="14px" />{" "}
                  {copied ? "Copied" : "Copy text"}
                </button>
              </div>
            )}
            {copyError && (
              <p className="errline" style={{ marginTop: 8 }}>
                {copyError}
              </p>
            )}
          </div>
          <div className="tabbody transcript-body" ref={bodyRef}>
            {loading && <p className="placeholder">Loading transcript…</p>}
            {error && <p className="errline">{error}</p>}
            {!loading && !error && cues.length === 0 && (
              <p className="placeholder">No transcript available.</p>
            )}
            {!loading && !error && cues.length > 0 && (
              <div className="transcript">
                {/* The row is prose, and the TIMESTAMP is the control.

                    It used to be the other way round: the whole row was a
                    <button>, so reading a line meant risking a jump, and — the
                    part that had no workaround — no browser extends a text
                    selection across two form controls, which made dragging over
                    several lines to copy them select nothing at all. The
                    `user-select: text` in index.css cannot reach that; only
                    taking the words out of a control can.

                    So the stamp that was already on every row carries the jump,
                    and the words are words. A transcript is read far more often
                    than it is jumped from, and the jump is now something aimed
                    at rather than triggered by touching the text. */}
                {cues.map((cue, i) => {
                  const stamp = formatDuration(cue.ts);
                  return (
                    <div
                      key={i}
                      data-cue={i}
                      className={`cue${hits.has(i) ? " hit" : ""}${
                        i === activeCue ? " current" : ""
                      }`}
                    >
                      {seek ? (
                        <button
                          type="button"
                          className="ts mono tseek"
                          onClick={seekOnClick(seek, cue.ts)}
                          title={`Play from ${stamp}`}
                          aria-label={`Play from ${stamp}`}
                        >
                          {stamp}
                        </button>
                      ) : (
                        // Nothing to jump to — the inbox video page has no
                        // media — so the stamp is plain text rather than a
                        // control that would do nothing.
                        <span className="ts mono">{stamp}</span>
                      )}
                      <span className="line">
                        {highlightCue(cue.text, find)}
                      </span>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
}
