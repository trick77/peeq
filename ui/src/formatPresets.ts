// PRESETS mirrors ytdlp.Presets exactly (backend/internal/ytdlp/format.go)
// plus the "custom" id Resolve special-cases — the format string shown
// under each preset must stay byte-for-byte in sync with that table.
//
// It lives here rather than in Settings because two screens choose from it:
// Settings picks the global preset, and a channel's Format override picks a
// per-channel one. Only Settings offers "custom" — a hand-written format
// string is set once, globally, never per channel — so consumers of the
// per-channel list filter it out via PRESETS_NO_CUSTOM below.
export type FormatPreset = { id: string; label: string; format: string };

export const PRESETS: FormatPreset[] = [
  {
    id: "apple-1080p",
    label: "Apple AirPlay 1080p",
    format: "bestvideo[height<=1080][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4",
  },
  {
    id: "apple-vp9-4k",
    label: "Apple VP9 4K",
    format:
      "bestvideo[height<=2160][vcodec*=vp9]+bestaudio[acodec*=mp4a]/bestvideo[height<=1080][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4",
  },
  {
    id: "best-mp4",
    label: "Best available MP4",
    format: "bestvideo+bestaudio/best",
  },
  { id: "custom", label: "Custom…", format: "write your own format string" },
];

export const PRESETS_NO_CUSTOM: FormatPreset[] = PRESETS.filter(
  (p) => p.id !== "custom",
);

// presetLabel names a stored preset id. It returns null for anything that
// isn't one — including a raw yt-dlp selector, which is what a channel's
// format_override held before the preset picker existed — so callers can
// tell "this is a preset" from "this is a string someone typed".
export function presetLabel(id: string): string | null {
  return PRESETS.find((p) => p.id === id)?.label ?? null;
}
