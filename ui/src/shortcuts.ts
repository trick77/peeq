// overlayOwnsKeys is the check every page-wide keyboard shortcut makes before
// acting: an open overlay owns the keyboard. Shared by SearchField's "/" and
// the Space play/pause so the two stand down in the same places.
//
// The modal case is asked of the document, not of the event target: a scrim
// click can leave focus on <body>, which no ancestor lookup from the target
// would catch. Popovers (RowMenu, the Share popover) are asked of the target
// instead — they are not modal, so one being open somewhere on the page is no
// reason to ignore a keystroke aimed outside it. Both park focus on their own
// container or first item, which is what the target lookup finds.
export function overlayOwnsKeys(target: EventTarget | null): boolean {
  if (document.querySelector('[role="dialog"][aria-modal="true"]')) return true;
  return (
    target instanceof Element &&
    target.closest('[role="menu"],[role="dialog"]') !== null
  );
}
