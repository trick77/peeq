import type { SponsorblockSegment } from "./components/Scrubber";

// NowPlaying is everything the dock needs to describe what is playing that it
// cannot read off the <video> element itself.
//
// Deliberately a snapshot of the video's FACTS, not of its playhead: the
// position changes ~4x a second, and routing that through App's state would
// re-render the whole shell at 4Hz. The dock listens to the element for time
// and paused state, and gets the rest from here — published once per video.
// No poster fields here, deliberately: the dock shows the live <video>, not a
// picture of it, so a thumbnail id and version would be carried across the app
// for nothing.
export type NowPlaying = {
  id: string;
  title: string;
  channelName: string;
  // Only until the element reports its own, which is the one the progress line
  // trusts — see NowDock.
  durationSeconds: number;
  segments: SponsorblockSegment[];
};
