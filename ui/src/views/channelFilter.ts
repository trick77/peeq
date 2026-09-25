import type { Channel } from "../api/types";
import type { ChannelFilter } from "../api/channels";

// channelMatchesFilter is the Channels page's chip filter, applied to the
// unfiltered list the page already holds. It mirrors channels.Store.List's
// WHERE clauses (backend/internal/channels/store.go) so a chip shows exactly
// what `?filter=` would have returned — the page used to ask the server once
// per chip click for a subset of a list it had in hand.
export function channelMatchesFilter(c: Channel, f: ChannelFilter): boolean {
  switch (f) {
    case "subscribed":
      return c.subscribed;
    case "notsubscribed":
      // added && !subscribed: the download-only rows have no subscription
      // either, and they are exactly what "downloaded" is for.
      return c.added && !c.subscribed;
    case "downloaded":
      return !c.added;
    case "autodownload":
      return c.autodownload;
    default:
      return true;
  }
}
