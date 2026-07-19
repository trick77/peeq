// nowPlaying — a tiny sessionStorage marker recording which video the Player
// currently holds and whether it is actively playing. It exists so a browser
// *reload* can reopen the Player on the video that was playing right before,
// continuing from the server-side resume position (Player already seeks to
// resume_position_seconds on load, so we deliberately do NOT autoplay — the
// video reopens paused at that position).
//
// Why sessionStorage and not localStorage: the marker is meant to survive a
// reload of the *same tab*, not to resurrect a video days later — closing the
// tab should forget it.
//
// Why "playing" gates the restore: React effect cleanups run on in-app
// navigation unmount but NOT on a real page reload/close. Player clears this
// marker in its unmount cleanup, so navigating away (to Library, delete, …)
// forgets it, while a reload leaves it intact. Combined with writing
// playing=false on pause/ended/metadata-load, the marker reads true only when
// a video was genuinely playing at the instant the page was torn down.

const KEY = "peeq.nowPlaying";

export type NowPlaying = { videoId: string; playing: boolean };

// readNowPlaying returns the stored marker, or null when absent/unparseable.
// Guarded so a disabled/unavailable sessionStorage (private mode, SSR) never
// throws into the App's render path.
export function readNowPlaying(): NowPlaying | null {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<NowPlaying>;
    if (typeof parsed.videoId !== "string" || typeof parsed.playing !== "boolean") {
      return null;
    }
    return { videoId: parsed.videoId, playing: parsed.playing };
  } catch {
    return null;
  }
}

// writeNowPlaying records the current video and its play state. Player calls
// this on play/pause/ended and once when metadata loads.
export function writeNowPlaying(videoId: string, playing: boolean): void {
  try {
    sessionStorage.setItem(KEY, JSON.stringify({ videoId, playing }));
  } catch {
    // ignore — a marker we can't persist just means no reload-restore.
  }
}

// clearNowPlaying forgets the marker. Player calls this on unmount so in-app
// navigation away from the Player never triggers a reload-restore.
export function clearNowPlaying(): void {
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    // ignore
  }
}
