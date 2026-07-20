// channelURLRe detects a channel URL client-side, mirroring the backend's
// ytdlp.Canonicalize kind switch (backend/internal/ytdlp/url.go): a
// youtube.com path starting with channel/, @, c/, or user/. Kept in sync by
// hand — Canonicalize is the source of truth, this is only a routing/
// validation hint so the UI can pick addChannel vs addDownload (and reject
// an obviously-wrong paste) before hitting the server.
//
// Shared by Add (which routes on it) and Channels (which validates its
// add-channel form with it) so the two can't drift apart independently.
export const channelURLRe = /youtube\.com\/(channel\/|@|c\/|user\/)/i;

export function isChannelURL(url: string): boolean {
  return channelURLRe.test(url.trim());
}
