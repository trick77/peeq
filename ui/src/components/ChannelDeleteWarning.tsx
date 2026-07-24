// ChannelDeleteWarning is the single source of the channel-delete warning copy,
// shared by the Channels list row and the channel detail Settings tab so the
// two delete flows can never drift in wording. The count differs by caller
// (the list knows downloaded_count, the detail knows archived_count) and is
// passed in; everything else — that files leave disk, that "kept forever"
// videos go too, that it cannot be undone — is fixed here.
export function ChannelDeleteWarning({
  name,
  count,
}: {
  name: string;
  count: number;
}) {
  return (
    <>
      Delete <b>{name}</b> and its {count} videos? This removes the files from
      disk, including any you kept forever. This cannot be undone.
    </>
  );
}
