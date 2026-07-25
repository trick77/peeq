import { api } from "./http";

// PlaybackState is the server-side "now playing" pointer: which video the user
// last opened in the Player. video_id is "" when nothing is playing — which
// covers both "never set" and "the target is no longer playable", since the
// backend filters a tombstoned or not-yet-downloaded video out.
//
// It lives server-side rather than in the URL or storage so it is a property of
// the account, not of one tab: open a video at the desk and the couch's rail
// opens the same thing. The URL stays the source of truth for which page is
// showing (see route.ts) — this only answers the question the URL can't, namely
// what "Now playing" means when the address bar carries no video id.
export type PlaybackState = { video_id: string; updated_at?: string };

export async function getPlaybackState(): Promise<PlaybackState> {
  return api.get<PlaybackState>("/api/playback", "failed to load now playing");
}

// setPlaybackState points "now playing" at a video, or clears it with null.
// Called once per video the Player opens — deliberately not on the resume ping,
// which would be hundreds of identical writes an hour.
export async function setPlaybackState(
  videoId: string | null,
): Promise<PlaybackState> {
  return api.put<PlaybackState>(
    "/api/playback",
    { video_id: videoId ?? "" },
    "failed to set now playing",
  );
}
