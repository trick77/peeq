// Channel — placeholder for the channel detail page reached by clicking a
// channel name (Task 11 wires up routing only; Tasks 12–14 build the real
// view: header, video list, in-page search).
export function Channel({
  channelId,
  onOpenVideo,
  onBack,
}: {
  channelId: string | null;
  onOpenVideo: (id: string) => void;
  onBack: () => void;
}) {
  if (!channelId) {
    return <p style={{ color: "var(--color-faint)" }}>No channel selected.</p>;
  }
  return <div data-testid="channel-page">{channelId}</div>;
}
