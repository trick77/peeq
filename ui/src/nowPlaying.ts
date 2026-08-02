import type { SponsorblockSegment } from "./components/Scrubber";

// NowPlaying is everything the dock needs to describe what is playing that it
// cannot read off the <video> element itself.
//
// Deliberately a snapshot of the video's FACTS, not of its playhead: the
// position changes ~4x a second, and routing that through App's state would
// re-render the whole shell at 4Hz. The dock listens to the element for time
// and paused state, and gets the rest from here — published once per video.
export type NowPlaying = {
  id: string;
  title: string;
  channelName: string;
  channelId: string;
  hasThumbnail: boolean;
  thumbnailVersion?: string;
  durationSeconds: number;
  segments: SponsorblockSegment[];
};
