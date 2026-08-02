// ChannelLink is the channel name in a video's byline: a link when there is
// somewhere to go, plain text when there is not.
//
// The same pair had been written out at four call sites — the library/archive
// card, the Inbox card, the Player's MetaHeader and the summary page an
// unfetched video opens to — and they drifted: the summary page's copy was
// still a bare span long after the other three learned to navigate. One
// component is what keeps that from happening again.
//
// onOpenChannel is optional because these views are reachable from places with
// nowhere to navigate to, and a dead button is worse than a label. channelId is
// checked too: a video peeq has not resolved a channel for has nothing to
// navigate WITH, and the name still has to render.
//
// Not used by the Channels list, whose name is a button inside its <h3> with a
// bare-text fallback and no .chan-name span — a different element in a
// different place, not a variant of this one.
export function ChannelLink({
  channelId,
  name,
  onOpenChannel,
}: {
  channelId: string;
  name?: string;
  onOpenChannel?: (channelId: string) => void;
}) {
  // The id is the fallback label, not a placeholder: it is the only handle a
  // channel has before its name resolves, and it is what the user sees.
  const label = name || channelId;

  return onOpenChannel && channelId ? (
    <button
      type="button"
      className="chan-link"
      onClick={() => onOpenChannel(channelId)}
    >
      {label}
    </button>
  ) : (
    <span className="chan-name">{label}</span>
  );
}
