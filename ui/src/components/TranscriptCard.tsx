import { useEffect, useState } from "react";
import { Icon } from "../icons";
import { formatDuration } from "../format";
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
}) {
  const [open, setOpen] = useState(false);
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
  const cues = loaded === vttUrl ? parsed : [];

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

  const hitCount = find
    ? cues.filter((c) => matchesFind(c.text, find)).length
    : 0;

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
        <Icon
          name="chevronRight"
          size="16px"
          style={{
            transition: "transform .15s",
            transform: open ? "rotate(90deg)" : "none",
          }}
        />
        <span className="lbl">Transcript</span>
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
              />
              <span className="count mono">
                {find ? `${hitCount} / ${cues.length}` : "—"}
              </span>
            </div>
            {cues.length > 0 && (
              <div className="transcript-dl">
                <span className="meta">Download</span>
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
          <div className="tabbody transcript-body">
            {loading && <p className="placeholder">Loading transcript…</p>}
            {error && <p className="errline">{error}</p>}
            {!loading && !error && cues.length === 0 && (
              <p className="placeholder">No transcript available.</p>
            )}
            {!loading && !error && cues.length > 0 && (
              <div className="transcript">
                {cues.map((cue, i) =>
                  seek ? (
                    <button
                      key={i}
                      type="button"
                      className={`cue${matchesFind(cue.text, find) ? " hit" : ""}`}
                      onClick={() => seek(cue.ts)}
                    >
                      <span className="ts mono">{formatDuration(cue.ts)}</span>
                      <span className="line">
                        {highlightCue(cue.text, find)}
                      </span>
                    </button>
                  ) : (
                    // No media to jump to, so the row is not a control. Same
                    // rule and the same class as the chapter and highlight
                    // rows: a button that looked identical and did nothing
                    // would be worse than plain text. Find still highlights it.
                    <div
                      key={i}
                      className={`cue inert${matchesFind(cue.text, find) ? " hit" : ""}`}
                    >
                      <span className="ts mono">{formatDuration(cue.ts)}</span>
                      <span className="line">
                        {highlightCue(cue.text, find)}
                      </span>
                    </div>
                  ),
                )}
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
}
