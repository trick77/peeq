import { useEffect, type RefObject } from "react";

// useDismiss closes an open popover on an outside click or Escape. Escape
// returns focus to the trigger; an outside click deliberately does not, since
// the click has already moved focus somewhere the user chose.
export function useDismiss(
  open: boolean,
  close: () => void,
  wrapRef: RefObject<HTMLElement | null>,
  triggerRef: RefObject<HTMLElement | null>,
): void {
  useEffect(() => {
    if (!open) return;
    function onDocClick(e: MouseEvent) {
      if (!wrapRef.current?.contains(e.target as Node)) close();
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        close();
        triggerRef.current?.focus();
      }
    }
    document.addEventListener("mousedown", onDocClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDocClick);
      document.removeEventListener("keydown", onKey);
    };
    // close is a state setter's wrapper at every call site; the refs are stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
}

// useFocusChecked focuses the checked row when a menu opens (the first row if
// none is checked), so the keyboard lands where the eye does.
export function useFocusChecked(
  open: boolean,
  menuRef: RefObject<HTMLElement | null>,
): void {
  useEffect(() => {
    if (!open) return;
    const menu = menuRef.current;
    if (!menu) return;
    const items = menu.querySelectorAll<HTMLButtonElement>("button");
    const checked = menu.querySelector<HTMLButtonElement>(
      'button[aria-checked="true"]',
    );
    (checked ?? items[0])?.focus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
}
