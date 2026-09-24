import { useCallback, useEffect, useRef, type MutableRefObject } from "react";
import { setResume } from "../../api/videos";
import { ApiError } from "../../api/http";

// How often the playhead position is written back while a video plays.
export const RESUME_THROTTLE_MS = 5000;

export type ResumeWriteMode = "ping" | "flush";

export type ResumeSyncOptions = {
  videoId: string | null;
  // Called when a write reports the video watched (the server's own >=90%
  // auto-mark) and nothing newer has spoken for the flag since — see
  // watchedEpochRef. The page adopts the flag from here.
  onAdoptWatched: (id: string) => void;
  // Called when a ping is refused with 409: the video's watched state changed
  // somewhere this page never saw. Only pings report it; a flush has no page
  // left to react (see writeResume).
  onConflict: (id: string) => void;
};

export type ResumeSync = {
  // The latest known playhead position, independent of the <video> node: on
  // unmount React nulls the element ref before the cleanup runs, so the node
  // is unreliable there and this is what the flush reads.
  positionRef: MutableRefObject<number>;
  // positionRef starts at 0, which is indistinguishable from "the user really
  // is at 0:00". Nothing is written until a real position has been observed,
  // or a flush before the first timeupdate would overwrite a stored resume
  // position with 0.
  positionKnownRef: MutableRefObject<boolean>;
  // The video's state_version as this page last saw it, echoed on every
  // resume write so a watched toggle made elsewhere can't be undone by this
  // client writing its stale position back (issue #97). null means "not
  // known yet". Refreshed from EVERY response that reports a version, not
  // just getVideo: the write's own auto-mark bumps it.
  stateVersionRef: MutableRefObject<number | null>;
  // Bumped by the watched toggle, captured by every write, checked before
  // adopting a watched flag: it invalidates whatever was in flight when the
  // button was pressed, so un-watching a video the auto-mark had just flipped
  // is not reversed by the very response that flipped it.
  watchedEpochRef: MutableRefObject<number>;
  // Where a jump put the playhead, or null when this page was not opened at
  // a moment. While it is set, no position is written: a jump nobody played
  // on from is not progress, and past the 90% line it would file the video as
  // watched. The page clears it once playback has moved on.
  jumpAnchorRef: MutableRefObject<number | null>;
  // Which video this page currently has open, readable from an async
  // continuation that resumed after the answer changed; null once unmounted.
  openVideoIdRef: MutableRefObject<string | null>;
  // Record an observed playhead position without writing it.
  notePosition: (seconds: number) => void;
  // Forget the position: the playhead was put back to 0:00 by a watched
  // toggle or a cross-device mark, and nothing may be written until a real
  // position has been observed again.
  forgetPosition: () => void;
  // Write a position now, outside the throttle: the video ran to its end.
  writeNow: (id: string, seconds: number) => void;
  // Write a position if the throttle window has passed and no jump is
  // pending. The page calls this on every timeupdate.
  throttledPing: (id: string, seconds: number) => void;
  // Make the next throttledPing write regardless of the window: for a pause
  // whose next tick is never coming.
  forceNextPing: () => void;
};

// useResumeSync owns everything about writing the playhead back: the refs the
// page and its handlers share, the one write path, the throttle, and the
// flush on leaving — the tab going hidden, the page unloading, this component
// unmounting, or the page switching to another video.
//
// The flush is keyed on the video ID, not on the video object: the object is
// replaced on every summary event, favorite toggle and category change, and
// each replacement used to run the cleanup and send a spurious write. Keying
// on the id also puts the flush for the OLD video in the cleanup that React
// runs before any effect for the new one — so the last few seconds of the
// video being left are written before the refs are reset for its successor.
export function useResumeSync(opts: ResumeSyncOptions): ResumeSync {
  const { videoId } = opts;
  const positionRef = useRef(0);
  const positionKnownRef = useRef(false);
  const stateVersionRef = useRef<number | null>(null);
  const watchedEpochRef = useRef(0);
  const jumpAnchorRef = useRef<number | null>(null);
  const openVideoIdRef = useRef<string | null>(null);
  const lastSentRef = useRef(0);
  // The page's callbacks change identity every render; the write path reads
  // the latest through a ref so nothing below has to depend on them.
  const optsRef = useRef(opts);
  useEffect(() => {
    optsRef.current = opts;
  });

  // writeResume is the single write path. An accepted write adopts the
  // version it hands back, whatever mode: past the 90% threshold the server
  // auto-marks watched and bumps state_version, so dropping the response would
  // leave the ref stale and make the very next ping 409 against this page's
  // own write. Guarded on openVideoIdRef so a late response can't write one
  // video's version into the ref after the user has moved to another.
  //
  // A 409 is reported only for a ping. A flush also runs from the unmount
  // cleanup, where there is no page left to toast and no playhead to rewind;
  // a refused flush simply means the position the server holds is right.
  const writeResume = useCallback(
    (id: string, seconds: number, mode: ResumeWriteMode) => {
      const epoch = watchedEpochRef.current;
      setResume(id, seconds, stateVersionRef.current ?? undefined)
        .then((res) => {
          if (openVideoIdRef.current !== id) return;
          stateVersionRef.current = res.state_version;
          // Only a true is adopted: writing a position can never un-watch a
          // video server-side, so a false carries no news — while a response
          // already in flight when the user pressed the toggle carries a stale
          // one. The epoch is the other half of that guard.
          if (res.watched && epoch === watchedEpochRef.current) {
            optsRef.current.onAdoptWatched(id);
          }
        })
        .catch((e: unknown) => {
          if (mode === "ping" && e instanceof ApiError && e.status === 409) {
            optsRef.current.onConflict(id);
          }
        });
    },
    [],
  );

  // All stable: they touch only refs, so the page can hand them to memoised
  // children (seek → the cards) without breaking the memo.
  const notePosition = useCallback((seconds: number) => {
    positionRef.current = seconds;
    positionKnownRef.current = true;
  }, []);

  const forgetPosition = useCallback(() => {
    positionRef.current = 0;
    positionKnownRef.current = false;
  }, []);

  const writeNow = useCallback(
    (id: string, seconds: number) => {
      lastSentRef.current = Date.now();
      notePosition(seconds);
      writeResume(id, seconds, "ping");
    },
    [notePosition, writeResume],
  );

  const forceNextPing = useCallback(() => {
    lastSentRef.current = 0;
  }, []);

  const throttledPing = useCallback(
    (id: string, seconds: number) => {
      const now = Date.now();
      if (
        jumpAnchorRef.current === null &&
        now - lastSentRef.current >= RESUME_THROTTLE_MS
      ) {
        lastSentRef.current = now;
        writeResume(id, seconds, "ping");
      }
    },
    [writeResume],
  );

  // Flush the latest position on leaving. The throttled ping can leave up to
  // RESUME_THROTTLE_MS of progress unwritten, and every way of leaving —
  // switching tabs, closing the page, clicking back to the Library, opening
  // another video — would otherwise discard it.
  useEffect(() => {
    if (!videoId) return;
    const id = videoId;
    function flush() {
      if (!positionKnownRef.current) return;
      // A jump nobody played on from is not progress worth saving — and past
      // the 90% line it is worse than nothing, since the server would file the
      // video as watched. This was the path that used to do it: land on a
      // moment near the end, leave at once, and the unmount flush marked it.
      if (jumpAnchorRef.current !== null) return;
      writeResume(id, positionRef.current, "flush");
    }
    document.addEventListener("visibilitychange", flush);
    window.addEventListener("pagehide", flush);
    return () => {
      document.removeEventListener("visibilitychange", flush);
      window.removeEventListener("pagehide", flush);
      flush();
    };
  }, [videoId, writeResume]);

  // Reset for the next video. Declared AFTER the flush effect: React runs the
  // cleanups of both in order before either body, so the flush above still
  // sees the old video's refs, and only then are they cleared here.
  useEffect(() => {
    positionKnownRef.current = false;
    stateVersionRef.current = null;
    // A fresh video's first tick writes at once; the previous video's throttle
    // window must not carry over and drop its first seconds.
    lastSentRef.current = 0;
    // Belongs to the video that was jumped into, so it goes with that video.
    // Left set, it would suppress the next video's writes until that one
    // happened to pass the same mark.
    jumpAnchorRef.current = null;
    openVideoIdRef.current = videoId;
    return () => {
      // Cleared on unmount as well as on a video change; the next run sets
      // the new id back immediately, so only a real teardown leaves it null.
      openVideoIdRef.current = null;
    };
  }, [videoId]);

  return {
    positionRef,
    positionKnownRef,
    stateVersionRef,
    watchedEpochRef,
    jumpAnchorRef,
    openVideoIdRef,
    notePosition,
    forgetPosition,
    writeNow,
    throttledPing,
    forceNextPing,
  };
}
